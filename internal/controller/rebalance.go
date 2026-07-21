/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"

	qdrantv1alpha1 "github.com/keiailab/qdrant-operator/api/v1alpha1"
	"github.com/keiailab/qdrant-operator/internal/qdrant"
)

// ── B-3 rebalance planner — 순수 함수 (관측 스냅샷 → 결정론 이동 계획) ──
//
// 설계 규칙(불변):
//   - 이동은 count[donor]-count[recipient] >= 2 일 때만 후보가 된다 → 각 이동이 Σcount²
//     를 엄격 감소시켜 유한 종료가 보장된다. 균형 목표 = 컬렉션별 max-min <= 1.
//   - distinct-peer 제약: recipient 가 이미 그 shard 의 replica 를 보유하면 후보에서 제외.
//   - 결정론 총순서: donor count 내림차순 → recipient count 오름차순 → donor id 내림차순
//     → recipient id 오름차순 → shard id 오름차순 → 컬렉션명 오름차순(최종 tie-break).
//     동일 관측이면 항상 동일한 계획이 나온다(무저장 재계획의 전제).
//   - planner 는 순수 관측 입력만 받고 어떤 호출도 하지 않는다 — 발행은 executor 몫.

// plannedMove 는 이동 후보 1건.
type plannedMove struct {
	Collection string
	ShardID    uint32
	From       uint64
	To         uint64
	// Drop=true 면 목적지 없는 잉여 복제본 드롭(drop_replica) — B-4 드레인 전용.
	Drop bool
	// Replicate=true 면 원본 잔존 복제 추가(replicate_shard) — RF 수리 전용(v0.4.0).
	Replicate bool
	// 정렬 키 스냅샷(후보 생성 시점의 donor/recipient shard 수)
	fromCount int
	toCount   int
}

// String 은 status 표기 — 이동 "coll/shard: from->to", 드롭 "coll/shard: drop@peer",
// 복제 "coll/shard: +replica from->to".
func (m plannedMove) String() string {
	if m.Drop {
		return fmt.Sprintf("%s/%d: drop@%d", m.Collection, m.ShardID, m.From)
	}
	if m.Replicate {
		return fmt.Sprintf("%s/%d: +replica %d->%d", m.Collection, m.ShardID, m.From, m.To)
	}
	return fmt.Sprintf("%s/%d: %d->%d", m.Collection, m.ShardID, m.From, m.To)
}

// observation 은 planner/executor 가 소비하는 관측 스냅샷(원시 peer id 유지).
type observation struct {
	Peers []qdrant.Peer // id 오름차순
	// 컬렉션명 → 분포. Transfers 가 하나라도 있으면 클러스터 전역에서 새 이동 발행 금지.
	Collections map[string]*qdrant.CollectionClusterInfo

	// ── v0.4.0 확장 ──
	// RF: 컬렉션 → qdrant 설정 replication_factor (재복제 목표 — CR 채택 여부 무관).
	RF map[string]uint32
	// Sizes: 컬렉션 → shardID → points. peer 별 local 관측 취합(remote 는 크기 미제공).
	Sizes map[string]map[uint32]uint64
	// SizesComplete=false 면 peer 관측 일부 실패 — 크기 단계 전체 스킵(부분 지도 오판 금지).
	SizesComplete bool
}

// transfersInFlight 는 전 컬렉션의 진행 중 전송 수 합.
func (o *observation) transfersInFlight() int {
	n := 0
	for _, cc := range o.Collections {
		n += len(cc.Transfers)
	}
	return n
}

// allShardsActive 는 비-Active shard(전이 중) 존재 여부 — 있으면 이번 cycle 계획 보류.
func (o *observation) allShardsActive() bool {
	for _, cc := range o.Collections {
		for _, s := range cc.Shards {
			if s.State != shardStateActive {
				return false
			}
		}
	}
	return true
}

// ── B-3 executor — 관측 게이트 → 완료/유실 판정 → 계획 선노출 → 동시 1건 발행 ──

const (
	// moveAppearDeadline: 발행(raft 커밋) 후 이동/드롭이 관측에 나타나야 하는 기한 —
	// 초과 시 lost-command 로 판정한다.
	moveAppearDeadline  = 60 * time.Second
	moveBaseBackoff     = 30 * time.Second
	moveMaxBackoff      = 10 * time.Minute
	moveMaxBackoffShift = 5 // 30s<<5=16m > 10m 상한 — 이 이상 지수는 무의미(wrap 방지 clamp)
	phaseRebalancing    = "Rebalancing"
	// settleActiveMove 반환 상태
	settleBusy         = "busy"
	settleWaiting      = "waiting"
	settleLost         = "lost"
	settleSettled      = "settled"
	reasonDrainBlocked = "DrainBlocked"
	phaseRunning       = "Running"
	phaseDraining      = "Draining"
	shardStateActive   = "Active"
	kindReplicate      = "Replicate"
	// 크기 가중 2차 기준 발동 임계(하드코드 — 설계 §① YAGNI): 비율과 절대차 모두 충족해야 이동.
	sizeImbalanceRatio = 2.0
	sizeMinDelta       = uint64(10_000)
)

// rebalanceEnabled — spec.rebalance.enabled 기본 true(활성). false = dry-run.
func rebalanceEnabled(qc *qdrantv1alpha1.QdrantCluster) bool {
	return qc.Spec.Rebalance == nil || qc.Spec.Rebalance.Enabled == nil || *qc.Spec.Rebalance.Enabled
}

// backoff 는 유실/실패 연속 횟수 level 의 재큐 지연 — 지수이되 상한·비음수 보장.
// shift clamp(5) + wrap 방어로 level 무한 증가에도 항상 (0, 10m] 이다(hot-loop 차단).
func backoff(level int32) time.Duration {
	exp := min(max(level-1, 0), moveMaxBackoffShift)
	d := min(moveBaseBackoff<<uint(exp), moveMaxBackoff)
	if d <= 0 { // int64 wrap 방어(이론상 도달 불가 — clamp 가 선행)
		d = moveMaxBackoff
	}
	return d
}

// activeMoveCompleted 는 move/drop 각각의 완료를 관측 기반으로 판정한다 — drop 이
// shard_transfers 에 나타나는지는 미검증 영역이라 배치 관측만 신뢰한다.
func activeMoveCompleted(obs *observation, am *qdrantv1alpha1.MoveStatus) bool {
	cc, ok := obs.Collections[am.Collection]
	if !ok {
		return false
	}
	from, _ := strconv.ParseUint(am.FromPeer, 10, 64)
	if am.Drop {
		// 드롭 완료 = source 가 더는 그 shard 를 보유하지 않음.
		for _, s := range cc.Shards {
			if int32(s.ShardID) == am.ShardID && s.PeerID == from {
				return false
			}
		}
		return true
	}
	to, _ := strconv.ParseUint(am.ToPeer, 10, 64)
	var toActive, fromHolds bool
	for _, s := range cc.Shards {
		if int32(s.ShardID) != am.ShardID {
			continue
		}
		if s.PeerID == to && s.State == shardStateActive {
			toActive = true
		}
		if s.PeerID == from {
			fromHolds = true
		}
	}
	if am.Kind == kindReplicate {
		// 복제 완료 = target 에 Active 등장. 원본은 잔존이 정상(fromHolds 무관).
		return toActive
	}
	return toActive && !fromHolds
}

// settleActiveMove — 재배치·드레인 공통 정산. 반환 state:
//
//	busy    — 전역 transfer in-flight: 새 발행 금지, 폴링
//	waiting — 발행 후 출현창(60s) 내 미관측: 대기(재발행 금지)
//	lost    — 유실 판정: 레코드를 nil 로 비워(★영구 정지 차단) 백오프 후 재계획 허용
//	settled — 완료 정산 또는 추적 없음: 호출자가 계획 단계로 진행
func (r *QdrantClusterReconciler) settleActiveMove(qc *qdrantv1alpha1.QdrantCluster, obs *observation) (string, time.Duration) {
	if obs.transfersInFlight() > 0 {
		return settleBusy, 10 * time.Second
	}
	am := qc.Status.ActiveMove
	if am == nil {
		return settleSettled, 0
	}
	switch {
	case activeMoveCompleted(obs, am):
		qc.Status.ActiveMove = nil
		qc.Status.MoveBackoff = 0
		meta.SetStatusCondition(&qc.Status.Conditions, metav1.Condition{Type: condDegraded, Status: metav1.ConditionFalse, Reason: "MoveCompleted", Message: fmt.Sprintf("%s/%d 정산 완료", am.Collection, am.ShardID), ObservedGeneration: qc.Generation})
		return settleSettled, 0
	case am.IssuedAt != nil && time.Since(am.IssuedAt.Time) < moveAppearDeadline:
		return settleWaiting, 5 * time.Second
	default:
		// lost-command — 레코드 전체를 비워 다음 cycle 이 반드시 재계획에 도달한다.
		// escalation 은 스칼라 MoveBackoff 가 보존한다.
		qc.Status.ActiveMove = nil
		qc.Status.MoveBackoff++
		d := backoff(qc.Status.MoveBackoff)
		meta.SetStatusCondition(&qc.Status.Conditions, metav1.Condition{Type: condDegraded, Status: metav1.ConditionTrue, Reason: "MoveFailed", Message: fmt.Sprintf("%s/%d 가 %s 내 관측되지 않음(lost-command) — %v 후 재계획", am.Collection, am.ShardID, moveAppearDeadline, d), ObservedGeneration: qc.Generation})
		r.Recorder.Event(qc, corev1.EventTypeWarning, "MoveFailed", "이동/드롭 명령 유실 — 재계획 예정")
		return settleLost, d
	}
}

// issueActiveMove — 선노출(발행 전 status 커밋) 후 단일 발행. 즉시 실패도 유실과 동일하게
// 레코드를 비워 stall 을 차단한다.
func (r *QdrantClusterReconciler) issueActiveMove(ctx context.Context, qc *qdrantv1alpha1.QdrantCluster, mv plannedMove, kind string, qcl qdrant.Client) time.Duration {
	now := metav1.Now()
	qc.Status.ActiveMove = &qdrantv1alpha1.MoveStatus{
		Kind: kind, Collection: mv.Collection, ShardID: int32(mv.ShardID),
		FromPeer: strconv.FormatUint(mv.From, 10), Drop: mv.Drop, IssuedAt: &now,
	}
	if !mv.Drop {
		qc.Status.ActiveMove.ToPeer = strconv.FormatUint(mv.To, 10)
	}
	if err := r.Status().Update(ctx, qc); err != nil {
		return 5 * time.Second
	}
	var err error
	switch {
	case mv.Drop:
		err = qcl.DropReplica(ctx, mv.Collection, mv.ShardID, mv.From)
	case mv.Replicate:
		err = qcl.ReplicateShard(ctx, mv.Collection, mv.ShardID, mv.From, mv.To)
	default:
		err = qcl.MoveShard(ctx, mv.Collection, mv.ShardID, mv.From, mv.To)
	}
	if err != nil {
		qc.Status.ActiveMove = nil
		qc.Status.MoveBackoff++
		meta.SetStatusCondition(&qc.Status.Conditions, metav1.Condition{Type: condDegraded, Status: metav1.ConditionTrue, Reason: "MoveFailed", Message: err.Error(), ObservedGeneration: qc.Generation})
		r.Recorder.Event(qc, corev1.EventTypeWarning, "MoveFailed", err.Error())
		return backoff(qc.Status.MoveBackoff)
	}
	reason := "ShardMoveIssued"
	switch {
	case mv.Drop:
		reason = "ReplicaDropIssued"
	case mv.Replicate:
		reason = "ShardReplicateIssued"
	}
	r.Recorder.Event(qc, corev1.EventTypeNormal, reason, mv.String())
	return 10 * time.Second
}

// reconcileRebalance 는 ready(전 replica 준비 + peer 합류 완료) 상태의 최종 status writer.
func (r *QdrantClusterReconciler) reconcileRebalance(ctx context.Context, qc *qdrantv1alpha1.QdrantCluster, obs *observation, qcl qdrant.Client) (string, time.Duration) {
	state, d := r.settleActiveMove(qc, obs)
	switch state {
	case settleBusy, settleWaiting, settleLost:
		return phaseRebalancing, d
	}

	// 비-Active shard(전이 중) — 계획 보류.
	if !obs.allShardsActive() {
		return phaseRebalancing, 10 * time.Second
	}

	// 신선 재계획 + 선노출 — 재복제(내구성) > 리밸런스(성능) 우선순위(설계 §②).
	plan := planReplications(obs)
	kind := kindReplicate
	if len(plan) == 0 {
		plan = planRebalance(obs)
		kind = "Rebalance"
	}
	qc.Status.PlannedMoves = nil
	for i, mv := range plan {
		if i >= 10 { // status 크기 상한
			break
		}
		qc.Status.PlannedMoves = append(qc.Status.PlannedMoves, mv.String())
	}
	if len(plan) == 0 {
		// 균형 — steady-state 무행동. 단 requeue 0(이벤트 대기만)이면 status 반영이 한 번
		// 유실될 때(라이브 사례: v0.3.0 기동이 CRD 갱신을 앞질러 신필드 status.selector 가
		// apiserver 에서 드롭) 재시도 이벤트가 없어 영구 미반영된다 — 완만한 주기로
		// 자가치유 + B-2 관측 신선도를 보장한다(GET 수 회 / 5m, 행동 없음 불변).
		return phaseRunning, 5 * time.Minute
	}
	if !rebalanceEnabled(qc) {
		return phaseRunning, 2 * time.Minute // dry-run: 계획만 노출(재복제 포함 — 완전 수동 모드 일관)
	}
	return phaseRebalancing, r.issueActiveMove(ctx, qc, plan[0], kind, qcl)
}

// requeueOrNothing 은 phase 결과에 따른 ctrl.Result 조립 헬퍼.
func requeueOrNothing(d time.Duration) ctrl.Result {
	if d > 0 {
		return ctrl.Result{RequeueAfter: d}
	}
	return ctrl.Result{}
}

// planRebalance 는 관측에서 이동 계획 전체를 재산출한다(무저장 재계획 — 호출자는
// 첫 항목 하나만 집행한다). 반환 계획이 비면 균형(또는 distinct-peer 제약 하 잔여).
func planRebalance(obs *observation) []plannedMove {
	if len(obs.Peers) < 2 {
		return nil
	}
	var plan []plannedMove

	collNames := make([]string, 0, len(obs.Collections))
	for name := range obs.Collections {
		collNames = append(collNames, name)
	}
	slices.Sort(collNames)

	for _, coll := range collNames {
		cc := obs.Collections[coll]
		// peer 별 shard 수 (0 포함 — 모든 합의 peer 가 후보)
		count := map[uint64]int{}
		for _, p := range obs.Peers {
			count[p.ID] = 0
		}
		// shard → 보유 peer 집합 (replica 인지 — distinct-peer 제약용)
		holders := map[uint32]map[uint64]bool{}
		for _, s := range cc.Shards {
			if _, known := count[s.PeerID]; known {
				count[s.PeerID]++
			}
			if holders[s.ShardID] == nil {
				holders[s.ShardID] = map[uint64]bool{}
			}
			holders[s.ShardID][s.PeerID] = true
		}

		// donor(초과) → recipient(부족) 후보 열거
		for _, donor := range obs.Peers {
			for _, recip := range obs.Peers {
				if donor.ID == recip.ID || count[donor.ID]-count[recip.ID] < 2 {
					continue
				}
				// donor 가 보유한 shard 중 recipient 에 replica 가 없는 최소 shard id
				var shardIDs []uint32
				for _, s := range cc.Shards {
					if s.PeerID == donor.ID && !holders[s.ShardID][recip.ID] {
						shardIDs = append(shardIDs, s.ShardID)
					}
				}
				if len(shardIDs) == 0 {
					continue // distinct-peer 제약으로 이 (donor,recip) 쌍은 이동 불가
				}
				slices.Sort(shardIDs)
				plan = append(plan, plannedMove{
					Collection: coll, ShardID: shardIDs[0],
					From: donor.ID, To: recip.ID,
					fromCount: count[donor.ID], toCount: count[recip.ID],
				})
			}
		}
	}

	// 결정론 총순서
	slices.SortFunc(plan, func(a, b plannedMove) int {
		if a.fromCount != b.fromCount {
			return b.fromCount - a.fromCount // donor count 내림차순
		}
		if a.toCount != b.toCount {
			return a.toCount - b.toCount // recipient count 오름차순
		}
		if a.From != b.From {
			if a.From > b.From { // donor id 내림차순
				return -1
			}
			return 1
		}
		if a.To != b.To {
			if a.To < b.To { // recipient id 오름차순
				return -1
			}
			return 1
		}
		if a.ShardID != b.ShardID {
			return int(a.ShardID) - int(b.ShardID) // shard id 오름차순
		}
		return strings.Compare(a.Collection, b.Collection) // 컬렉션명 오름차순
	})
	if len(plan) == 0 {
		// count 균형(또는 distinct-peer 잔여) — 크기 가중 2차 단계(설계 §①).
		return planSizeRebalance(obs)
	}
	return plan
}

// planSizeRebalance 는 count 균형 상태에서 peer 총 points 불균형(비율≥2 ∧ 절대차≥10k)
// 을 고치는 이동 1건을 산출한다. 이동 후 max−min 이 엄격 감소하는 후보만 채택(과교정·
// 진동 구조적 배제) — 관측 불완전(SizesComplete=false)이면 전체 스킵.
func planSizeRebalance(obs *observation) []plannedMove {
	if len(obs.Peers) < 2 || !obs.SizesComplete {
		return nil
	}
	// peer 총 points (전 컬렉션 합산 — 배치 균형은 노드 단위 자원 문제).
	total := map[uint64]uint64{}
	for _, p := range obs.Peers {
		total[p.ID] = 0
	}
	for coll, cc := range obs.Collections {
		for _, s := range cc.Shards {
			if _, known := total[s.PeerID]; known {
				total[s.PeerID] += obs.Sizes[coll][s.ShardID]
			}
		}
	}
	spread := func(t map[uint64]uint64) uint64 {
		var mx, mn uint64 = 0, ^uint64(0)
		for _, v := range t {
			mx, mn = max(mx, v), min(mn, v)
		}
		return mx - mn
	}
	var donor, recip uint64
	var mx uint64
	mn := ^uint64(0)
	for _, p := range obs.Peers { // Peers 는 id 오름차순 — 동률 시 결정론
		if total[p.ID] > mx {
			mx, donor = total[p.ID], p.ID
		}
		if total[p.ID] < mn {
			mn, recip = total[p.ID], p.ID
		}
	}
	if donor == recip || mx-mn < sizeMinDelta || float64(mx) < sizeImbalanceRatio*float64(max(mn, 1)) {
		return nil
	}
	// donor 보유 shard 중 recipient 에 replica 없고 이동 후 spread 가 엄격 감소하는 후보.
	type cand struct {
		coll      string
		shardID   uint32
		newSpread uint64
	}
	var best *cand
	collNames := make([]string, 0, len(obs.Collections))
	for name := range obs.Collections {
		collNames = append(collNames, name)
	}
	slices.Sort(collNames)
	for _, coll := range collNames {
		cc := obs.Collections[coll]
		holders := map[uint32]map[uint64]bool{}
		for _, s := range cc.Shards {
			if holders[s.ShardID] == nil {
				holders[s.ShardID] = map[uint64]bool{}
			}
			holders[s.ShardID][s.PeerID] = true
		}
		for _, s := range cc.Shards {
			if s.PeerID != donor || holders[s.ShardID][recip] {
				continue
			}
			size := obs.Sizes[coll][s.ShardID]
			t2 := maps.Clone(total)
			t2[donor] -= size
			t2[recip] += size
			ns := spread(t2)
			if ns >= mx-mn { // 엄격 감소 아님(빈 shard·과교정) — 배제
				continue
			}
			if best == nil || ns < best.newSpread ||
				(ns == best.newSpread && (coll < best.coll || (coll == best.coll && s.ShardID < best.shardID))) {
				best = &cand{coll: coll, shardID: s.ShardID, newSpread: ns}
			}
		}
	}
	if best == nil {
		return nil
	}
	return []plannedMove{{Collection: best.coll, ShardID: best.shardID, From: donor, To: recip}}
}

// planReplications 는 RF 부족 shard 의 복제 계획을 산출한다(내구성 수리 — 리밸런스보다
// 우선). 목표 RF = qdrant 컬렉션 설정값(obs.RF). peers < RF 면 발행 불가(관측만) —
// RF 초과분 자동 drop 은 없다(파괴 동작 불가 원칙).
func planReplications(obs *observation) []plannedMove {
	var plan []plannedMove
	collNames := make([]string, 0, len(obs.Collections))
	for name := range obs.Collections {
		collNames = append(collNames, name)
	}
	slices.Sort(collNames)
	for _, coll := range collNames {
		rf := obs.RF[coll]
		if rf < 2 || uint32(len(obs.Peers)) < rf {
			continue
		}
		cc := obs.Collections[coll]
		count := map[uint64]int{} // peer 별 이 컬렉션 shard 보유 수(target 선택 부하 기준)
		for _, p := range obs.Peers {
			count[p.ID] = 0
		}
		holders := map[uint32][]uint64{} // shardID → Active 보유 peer(오름차순 아님 — 후처리 정렬)
		for _, s := range cc.Shards {
			if _, known := count[s.PeerID]; known {
				count[s.PeerID]++
			}
			if s.State == shardStateActive {
				holders[s.ShardID] = append(holders[s.ShardID], s.PeerID)
			}
		}
		shardIDs := make([]uint32, 0, len(holders))
		for sid := range holders {
			shardIDs = append(shardIDs, sid)
		}
		slices.Sort(shardIDs)
		for _, sid := range shardIDs {
			hs := holders[sid]
			if uint32(len(hs)) >= rf {
				continue
			}
			slices.Sort(hs)
			source := hs[0] // 보유 최소 peerID — 결정론
			// target: 미보유 peer 중 (shard 수, peerID) 오름차순 첫 번째.
			var target uint64
			found := false
			for _, p := range obs.Peers {
				if slices.Contains(hs, p.ID) {
					continue
				}
				if !found || count[p.ID] < count[target] || (count[p.ID] == count[target] && p.ID < target) {
					target, found = p.ID, true
				}
			}
			if !found {
				continue
			}
			plan = append(plan, plannedMove{Collection: coll, ShardID: sid, From: source, To: target, Replicate: true})
		}
	}
	return plan
}
