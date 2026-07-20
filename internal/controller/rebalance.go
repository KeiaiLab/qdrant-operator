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
	// 정렬 키 스냅샷(후보 생성 시점의 donor/recipient shard 수)
	fromCount int
	toCount   int
}

// String 은 status.plannedMoves 표기("collection/shard: from->to").
func (m plannedMove) String() string {
	return fmt.Sprintf("%s/%d: %d->%d", m.Collection, m.ShardID, m.From, m.To)
}

// observation 은 planner/executor 가 소비하는 관측 스냅샷(원시 peer id 유지).
type observation struct {
	Peers []qdrant.Peer // id 오름차순
	// 컬렉션명 → 분포. Transfers 가 하나라도 있으면 클러스터 전역에서 새 이동 발행 금지.
	Collections map[string]*qdrant.CollectionClusterInfo
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
	// moveAppearDeadline: MoveShard 200(raft 커밋) 후 이동이 관측(transfer 또는 배치 변화)에
	// 나타나야 하는 기한 — 초과 시 lost-command 로 판정한다.
	moveAppearDeadline = 60 * time.Second
	phaseRebalancing   = "Rebalancing"
	phaseRunning       = "Running"
	shardStateActive   = "Active"
)

// rebalanceEnabled — spec.rebalance.enabled 기본 true(활성). false = dry-run.
func rebalanceEnabled(qc *qdrantv1alpha1.QdrantCluster) bool {
	return qc.Spec.Rebalance == nil || qc.Spec.Rebalance.Enabled == nil || *qc.Spec.Rebalance.Enabled
}

// moveCompleted — move 시맨틱(완료 후 source 삭제) 기준: to 가 해당 shard 를 Active 로
// 보유하고 from 이 더는 보유하지 않을 때만 완료.
func moveCompleted(obs *observation, cm *qdrantv1alpha1.RebalanceMoveStatus) bool {
	cc, ok := obs.Collections[cm.Collection]
	if !ok {
		return false
	}
	from, _ := strconv.ParseUint(cm.FromPeer, 10, 64)
	to, _ := strconv.ParseUint(cm.ToPeer, 10, 64)
	var toActive, fromHolds bool
	for _, s := range cc.Shards {
		if int32(s.ShardID) != cm.ShardID {
			continue
		}
		if s.PeerID == to && s.State == shardStateActive {
			toActive = true
		}
		if s.PeerID == from {
			fromHolds = true
		}
	}
	return toActive && !fromHolds
}

// reconcileRebalance 는 ready(전 replica 준비 + peer 합류 완료) 상태의 최종 status writer.
// 반환: (phase, requeue) — 호출자가 phase 를 반영하고 Status().Update 후 requeue 한다.
func (r *QdrantClusterReconciler) reconcileRebalance(ctx context.Context, qc *qdrantv1alpha1.QdrantCluster, obs *observation, qcl qdrant.Client) (string, time.Duration) {
	// 1) 진행 중 전송 — 새 발행 금지, 완료 폴링만 (동시 1건은 오퍼레이터 메모리가 아니라
	//    Qdrant 관측으로 강제되어 재시작·리더 전환에도 유지된다).
	if obs.transfersInFlight() > 0 {
		return phaseRebalancing, 10 * time.Second
	}

	// 2) 발행 추적(CurrentMove) 정산 — transfers 는 비어 있다.
	if cm := qc.Status.Rebalance; cm != nil {
		switch {
		case moveCompleted(obs, cm):
			qc.Status.Rebalance = nil
			meta.SetStatusCondition(&qc.Status.Conditions, metav1.Condition{Type: condDegraded, Status: metav1.ConditionFalse, Reason: "MoveCompleted", Message: fmt.Sprintf("%s/%d 이동 완료", cm.Collection, cm.ShardID), ObservedGeneration: qc.Generation})
		case cm.IssuedAt != nil && time.Since(cm.IssuedAt.Time) < moveAppearDeadline:
			// raft 커밋 직후 관측 반영 전 창 — 재발행 금지, 대기.
			return phaseRebalancing, 5 * time.Second
		default:
			// lost-command — 백오프 후 재계획(다음 cycle 에서 신선 관측으로 재산출).
			cm.FailureCount++
			delay := min(30*time.Second*(1<<uint(cm.FailureCount-1)), 10*time.Minute)
			meta.SetStatusCondition(&qc.Status.Conditions, metav1.Condition{Type: condDegraded, Status: metav1.ConditionTrue, Reason: "MoveFailed", Message: fmt.Sprintf("%s/%d %s->%s 이동이 %s 내 관측되지 않음(lost-command) — %v 후 재계획", cm.Collection, cm.ShardID, cm.FromPeer, cm.ToPeer, moveAppearDeadline, delay), ObservedGeneration: qc.Generation})
			r.Recorder.Event(qc, corev1.EventTypeWarning, "MoveFailed", "이동 명령 유실 — 재계획 예정")
			cm.IssuedAt = nil // 다음 cycle 재계획 허용(FailureCount 는 백오프용으로 유지)
			return phaseRebalancing, delay
		}
	}

	// 3) 비-Active shard(전이 중) — 계획 보류.
	if !obs.allShardsActive() {
		return phaseRebalancing, 10 * time.Second
	}

	// 4) 신선 재계획 + 선노출.
	plan := planRebalance(obs)
	qc.Status.PlannedMoves = nil
	for i, mv := range plan {
		if i >= 10 { // status 크기 상한 — 전체 계획이 아니라 앞 10건만 노출
			break
		}
		qc.Status.PlannedMoves = append(qc.Status.PlannedMoves, mv.String())
	}
	if len(plan) == 0 {
		return phaseRunning, 0 // 균형 — steady-state 무행동
	}
	if !rebalanceEnabled(qc) {
		// dry-run: 계획만 노출, 발행 없음.
		return phaseRunning, 2 * time.Minute
	}

	// 5) 동시 1건 발행 — 선노출을 위해 발행 "전에" 추적 상태를 기록한다. 발행 실패 시
	//    추적을 지워 다음 cycle 재계획.
	mv := plan[0]
	now := metav1.Now()
	fc := int32(0)
	if qc.Status.Rebalance != nil {
		fc = qc.Status.Rebalance.FailureCount
	}
	qc.Status.Rebalance = &qdrantv1alpha1.RebalanceMoveStatus{
		Collection: mv.Collection, ShardID: int32(mv.ShardID),
		FromPeer: strconv.FormatUint(mv.From, 10), ToPeer: strconv.FormatUint(mv.To, 10),
		IssuedAt: &now, FailureCount: fc,
	}
	if err := r.Status().Update(ctx, qc); err != nil {
		return phaseRebalancing, 5 * time.Second
	}
	if err := qcl.MoveShard(ctx, mv.Collection, mv.ShardID, mv.From, mv.To); err != nil {
		qc.Status.Rebalance.IssuedAt = nil
		qc.Status.Rebalance.FailureCount++
		meta.SetStatusCondition(&qc.Status.Conditions, metav1.Condition{Type: condDegraded, Status: metav1.ConditionTrue, Reason: "MoveFailed", Message: err.Error(), ObservedGeneration: qc.Generation})
		r.Recorder.Event(qc, corev1.EventTypeWarning, "MoveFailed", err.Error())
		return phaseRebalancing, 30 * time.Second
	}
	r.Recorder.Event(qc, corev1.EventTypeNormal, "ShardMoveIssued", mv.String())
	return phaseRebalancing, 10 * time.Second
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
	return plan
}
