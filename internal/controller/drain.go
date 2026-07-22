/*
Copyright 2026 Keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package controller

import (
	"context"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	commonsevents "github.com/keiailab/keiailab-commons/pkg/events"
	qdrantv1alpha1 "github.com/keiailab/qdrant-operator/api/v1alpha1"
	"github.com/keiailab/qdrant-operator/internal/qdrant"
)

// ── B-4 Scale-in Drain — 거부 가드를 안전 절차로 대체 ──
//
// 불변식(§8.5): 서수 0 불가침 · 관측 불가 시 축소 안 함 · 어떤 실패 경로도 STS 축소 금지 ·
// RemovePeer 는 항상 force=false(qdrant "shard 잔존 시 거부"가 마지막 안전망) ·
// 마지막 활성 복제본 유실 금지 · 순서 = 이동/드롭 → RemovePeer → 축소.

// peerOrdinal 은 peer URI(http://<name>-<ordinal>.<name>-headless:6335/)에서 서수를 역산한다.
func peerOrdinal(uri string) (int32, bool) {
	u, err := url.Parse(uri)
	if err != nil || u.Hostname() == "" {
		return 0, false
	}
	firstLabel, _, _ := strings.Cut(u.Hostname(), ".")
	idx := strings.LastIndex(firstLabel, "-")
	if idx < 0 || idx == len(firstLabel)-1 {
		return 0, false
	}
	n, err := strconv.ParseInt(firstLabel[idx+1:], 10, 32)
	if err != nil || n < 0 {
		return 0, false
	}
	return int32(n), true
}

// peerOrdinals 는 모든 peer 의 서수를 해석한다. 하나라도 파싱 실패하면 ok=false —
// "이미 제거됨" 오판으로 무검증 축소하는 것을 차단한다(§8.2).
func peerOrdinals(peers []qdrant.Peer) (map[int32]qdrant.Peer, bool) {
	byOrdinal := map[int32]qdrant.Peer{}
	for _, p := range peers {
		ord, good := peerOrdinal(p.URI)
		if !good {
			return nil, false
		}
		byOrdinal[ord] = p
	}
	return byOrdinal, true
}

// drainPlan 은 한 pass 의 대상 peer 처분 계획(순수 함수 산출).
type drainPlan struct {
	Moves   []plannedMove // 이동(또는 드롭) 후보 — 결정론 정렬
	Blocked bool          // keeper 가 받지도 보유하지도 못함(마지막 복제본 유실 위험)
	Wait    bool          // 진행 중 transfer — 새 발행 보류
}

// planPeerDrain 은 대상 peer 의 모든 shard 처분을 계획한다: (1) 미보유 최소부하 keeper 로
// Move (2) 모든 keeper 보유(RF>1) 시 잉여 Drop (3) 불가 시 Blocked. keeper = 서수 < 목표.
func planPeerDrain(target qdrant.Peer, keepers []qdrant.Peer, colls map[string]*qdrant.CollectionClusterInfo) drainPlan {
	var plan drainPlan
	// keeper 부하(전 컬렉션 shard 수) — 최소부하 우선 배치의 결정론 기반.
	load := map[uint64]int{}
	for _, k := range keepers {
		load[k.ID] = 0
	}
	collNames := make([]string, 0, len(colls))
	for name := range colls {
		collNames = append(collNames, name)
	}
	slices.Sort(collNames)
	for _, name := range collNames {
		for _, s := range colls[name].Shards {
			if _, isKeeper := load[s.PeerID]; isKeeper {
				load[s.PeerID]++
			}
		}
		if len(colls[name].Transfers) > 0 {
			plan.Wait = true
		}
	}
	if plan.Wait {
		return plan
	}

	for _, name := range collNames {
		cc := colls[name]
		// shard → 보유 peer 집합
		holders := map[uint32]map[uint64]bool{}
		for _, s := range cc.Shards {
			if holders[s.ShardID] == nil {
				holders[s.ShardID] = map[uint64]bool{}
			}
			holders[s.ShardID][s.PeerID] = true
		}
		for _, s := range cc.Shards {
			if s.PeerID != target.ID {
				continue
			}
			// (1) 미보유 keeper 중 최소부하 (동률 시 peer id 오름차순 — 결정론)
			var best *qdrant.Peer
			for i := range keepers {
				k := keepers[i]
				if holders[s.ShardID][k.ID] {
					continue
				}
				if best == nil || load[k.ID] < load[best.ID] || (load[k.ID] == load[best.ID] && k.ID < best.ID) {
					best = &keepers[i]
				}
			}
			switch {
			case best != nil:
				plan.Moves = append(plan.Moves, plannedMove{Collection: name, ShardID: s.ShardID, From: target.ID, To: best.ID})
				load[best.ID]++
			case len(holders[s.ShardID]) >= 2:
				// (2) 모든 keeper 가 이미 보유 — 대상의 잉여 복제본 드롭(마지막 복제본 아님).
				plan.Moves = append(plan.Moves, plannedMove{Collection: name, ShardID: s.ShardID, From: target.ID, Drop: true})
			default:
				// (3) 위상상 도달 불가(목표>=1 이면 keeper 존재)이나 방어적으로 차단.
				plan.Blocked = true
			}
		}
	}
	return plan
}

// publishDrainStatus 는 계획·진척을 선노출한다(실행 전 status 커밋 — 관측 가능성).
func (r *QdrantClusterReconciler) publishDrainStatus(ctx context.Context, qc *qdrantv1alpha1.QdrantCluster, cur, tgt int32, target *qdrant.Peer, plan *drainPlan, msg string) {
	ds := qc.Status.DrainStatus
	if ds == nil {
		ds = &qdrantv1alpha1.DrainStatus{StartedAt: metav1.Now()}
		qc.Status.DrainStatus = ds
	}
	ds.TargetReplicas, ds.CurrentReplicas, ds.Message = tgt, cur, msg
	ds.Peers, ds.PendingMoves = nil, nil
	if target != nil {
		ds.Peers = []string{strconv.FormatUint(target.ID, 10)}
	}
	if plan != nil {
		for i, mv := range plan.Moves {
			if i >= 10 {
				break
			}
			ds.PendingMoves = append(ds.PendingMoves, mv.String())
		}
	}
	qc.Status.Phase = phaseDraining
	_ = r.Status().Update(ctx, qc)
}

// reshardActive 는 이 클러스터를 참조하는 QdrantCollection 중 리샤드 진행분이 있는지 —
// 전역 "단일 무거운 연산" 상호 deference(읽기전용) 게이트.
func (r *QdrantClusterReconciler) reshardActive(ctx context.Context, qc *qdrantv1alpha1.QdrantCluster) bool {
	list := &qdrantv1alpha1.QdrantCollectionList{}
	if err := r.List(ctx, list, client.InNamespace(qc.Namespace)); err != nil {
		return false
	}
	for i := range list.Items {
		col := &list.Items[i]
		if col.Spec.ClusterRef == qc.Name && col.Status.Reshard != nil {
			return true
		}
	}
	return false
}

// shrinkOne 은 liveSTS replicas 를 정확히 1 서수 낮춘다 — 목표까지 한 번에 밀지 않는다.
func (r *QdrantClusterReconciler) shrinkOne(ctx context.Context, live *appsv1.StatefulSet, to int32) error {
	live.Spec.Replicas = &to
	return r.Update(ctx, live)
}

// reconcileDrainCycle 은 드레인 한 pass(§8.4). top-level 가드가 cur>tgt 를 보장한다.
// 드레인 미완 구간에는 STS 를 apply 하지 않아 현 크기가 유지된다(replicas override 함정 회피).
func (r *QdrantClusterReconciler) reconcileDrainCycle(ctx context.Context, qc *qdrantv1alpha1.QdrantCluster, live *appsv1.StatefulSet) (ctrl.Result, error) {
	cur, tgt := *live.Spec.Replicas, qc.Spec.Replicas

	// 관측-우선: 드레인 경로도 B-2 status 를 갱신한다(관측 계약 유지, R1-1).
	obs := r.observe(ctx, qc)
	if obs == nil {
		r.publishDrainStatus(ctx, qc, cur, tgt, nil, nil, "qdrant 관측 불가 — 축소 보류")
		meta.SetStatusCondition(&qc.Status.Conditions, metav1.Condition{Type: condDegraded, Status: metav1.ConditionTrue, Reason: reasonDrainBlocked, Message: "관측 불가 시 축소하지 않음", ObservedGeneration: qc.Generation})
		_ = r.Status().Update(ctx, qc)
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}
	qc.Status.Replicas, qc.Status.ReadyReplicas = live.Status.Replicas, live.Status.ReadyReplicas

	// 통합 lane 정산(§7) — busy/waiting/lost 는 드레인도 동일하게 대기/백오프.
	if state, d := r.settleActiveMove(qc, obs); state != settleSettled {
		r.publishDrainStatus(ctx, qc, cur, tgt, nil, nil, "이동/드롭 정산 대기("+state+")")
		return ctrl.Result{RequeueAfter: d}, nil
	}

	// 전역 배타: 진행 중 리샤드가 있으면 이동을 발행하지 않는다(비대칭 우선순위 — 교착 없음).
	if r.reshardActive(ctx, qc) {
		r.publishDrainStatus(ctx, qc, cur, tgt, nil, nil, "리샤드 진행 중 — 드레인 이동 유예")
		return ctrl.Result{RequeueAfter: 15 * time.Second}, nil
	}

	ordinals, ok := peerOrdinals(obs.Peers)
	if !ok {
		r.publishDrainStatus(ctx, qc, cur, tgt, nil, nil, "peer URI 서수 해석 실패 — 무검증 축소 금지")
		meta.SetStatusCondition(&qc.Status.Conditions, metav1.Condition{Type: condDegraded, Status: metav1.ConditionTrue, Reason: reasonDrainBlocked, Message: "PeerURIUnparseable — 축소 보류", ObservedGeneration: qc.Generation})
		_ = r.Status().Update(ctx, qc)
		return ctrl.Result{RequeueAfter: time.Minute}, nil
	}

	targetPeer, present := ordinals[cur-1]
	if !present {
		// 대상 서수가 합의에 이미 없음(전 pass 에서 RemovePeer 완료) — 축소만 재개(멱등).
		if err := r.shrinkOne(ctx, live, cur-1); err != nil {
			return ctrl.Result{}, err
		}
		commonsevents.Emitf(r.Recorder, qc, "DrainShrunk", "replicas %d→%d", cur, cur-1)
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}

	if !obs.allShardsActive() {
		r.publishDrainStatus(ctx, qc, cur, tgt, &targetPeer, nil, "비-Active shard 전이 중 — 대기")
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}

	// keeper = 서수 < 목표 replicas (제거 예정 서수들은 수용처가 아니다).
	var keepers []qdrant.Peer
	for ord, p := range ordinals {
		if ord < tgt {
			keepers = append(keepers, p)
		}
	}
	slices.SortFunc(keepers, func(a, b qdrant.Peer) int {
		if a.ID < b.ID {
			return -1
		} else if a.ID > b.ID {
			return 1
		}
		return 0
	})

	plan := planPeerDrain(targetPeer, keepers, obs.Collections)
	r.publishDrainStatus(ctx, qc, cur, tgt, &targetPeer, &plan, "드레인 진행")

	switch {
	case plan.Blocked:
		meta.SetStatusCondition(&qc.Status.Conditions, metav1.Condition{Type: condDegraded, Status: metav1.ConditionTrue, Reason: reasonDrainBlocked, Message: "이동 목적지 없음(마지막 복제본 유실 위험) — 축소 보류", ObservedGeneration: qc.Generation})
		_ = r.Status().Update(ctx, qc)
		return ctrl.Result{RequeueAfter: time.Minute}, nil
	case plan.Wait:
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	case len(plan.Moves) > 0:
		d := r.issueActiveMove(ctx, qc, plan.Moves[0], "Drain", r.QdrantClientFor(qc))
		return ctrl.Result{RequeueAfter: d}, nil
	default:
		// 대상 peer 가 완전히 비었음 — 합의 제거(force=false 고정) 후에만 축소.
		if err := r.QdrantClientFor(qc).RemovePeer(ctx, targetPeer.ID, false); err != nil {
			qc.Status.MoveBackoff++
			meta.SetStatusCondition(&qc.Status.Conditions, metav1.Condition{Type: condDegraded, Status: metav1.ConditionTrue, Reason: reasonDrainBlocked, Message: "RemovePeer 실패: " + err.Error(), ObservedGeneration: qc.Generation})
			_ = r.Status().Update(ctx, qc)
			return ctrl.Result{RequeueAfter: backoff(qc.Status.MoveBackoff)}, nil
		}
		commonsevents.Emit(r.Recorder, qc, "DrainPeerRemoved", strconv.FormatUint(targetPeer.ID, 10))
		if err := r.shrinkOne(ctx, live, cur-1); err != nil {
			return ctrl.Result{}, err
		}
		commonsevents.Emitf(r.Recorder, qc, "DrainShrunk", "replicas %d→%d", cur, cur-1)
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}
}
