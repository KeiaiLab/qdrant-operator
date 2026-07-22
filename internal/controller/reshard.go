/*
Copyright 2026 Keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"

	commonsevents "github.com/keiailab/keiailab-commons/pkg/events"
	qdrantv1alpha1 "github.com/keiailab/qdrant-operator/api/v1alpha1"
	"github.com/keiailab/qdrant-operator/internal/qdrant"
)

// ── B-5 Re-shard 워크플로 — shadow 컬렉션 + scroll/upsert 복사 + alias 원자 스왑 ──
//
// 커밋점 = alias 스왑(§9.5). 스왑 이전 실패는 전부 가역: shadow 는 alias 로 외부 참조된
// 적 없는 오퍼레이터 소유 임시 컬렉션이라 폐기하고 원본을 무손상으로 남긴다.

const (
	reshardCopyBatch = 128 // reconcile 당 복사 배치 상한 — 부하 상수화(병렬 upsert 없음)
	phaseResharding  = "Resharding"
)

// resolvedShardNumber — nil 은 "라이브 값 채택"(생성 시 1): 절대 리샤드 트리거가 아니다.
func resolvedShardNumber(col *qdrantv1alpha1.QdrantCollection, live qdrant.CollectionInfo) uint32 {
	if col.Spec.ShardNumber != nil {
		return *col.Spec.ShardNumber
	}
	if live.Exists {
		return live.ShardNumber
	}
	return 1
}

// reshardable — size/distance/RF 일치 + shardNumber 만 명시적으로 상이할 때만.
func reshardable(live qdrant.CollectionInfo, col *qdrantv1alpha1.QdrantCollection) bool {
	return col.Spec.ShardNumber != nil && live.Exists &&
		live.VectorSize == col.Spec.Vectors.Size && live.Distance == col.Spec.Vectors.Distance &&
		live.ReplicationFactor == col.Spec.ReplicationFactor && live.ShardNumber != *col.Spec.ShardNumber
}

// physicalName — 이 CR 을 현재 뒷받침하는 물리 컬렉션(리샤드 스왑마다 갱신).
func physicalName(col *qdrantv1alpha1.QdrantCollection) string {
	if col.Status.ActiveCollection != "" {
		return col.Status.ActiveCollection
	}
	return col.TargetCollectionName()
}

// ensureAlias 는 alias→physical 정렬을 멱등 보장한다(steady 경로).
func (r *QdrantCollectionReconciler) ensureAlias(ctx context.Context, qcl qdrant.Client, alias, physical string) error {
	if alias == "" {
		return nil
	}
	aliases, err := qcl.ListAliases(ctx)
	if err != nil {
		return err
	}
	if aliases[alias] == physical {
		return nil
	}
	var actions []qdrant.AliasAction
	if _, exists := aliases[alias]; exists {
		actions = append(actions, qdrant.AliasAction{DeleteAlias: &qdrant.DeleteAlias{AliasName: alias}})
	}
	actions = append(actions, qdrant.AliasAction{CreateAlias: &qdrant.CreateAlias{AliasName: alias, CollectionName: physical}})
	return qcl.UpdateAliases(ctx, actions)
}

// clusterMoveActive — 전역 배타(§2.2): 클러스터 lane 에 발행 중 이동이 있으면 백필 유예.
func (r *QdrantCollectionReconciler) clusterMoveActive(cluster *qdrantv1alpha1.QdrantCluster) bool {
	return cluster.Status.ActiveMove != nil || cluster.Status.DrainStatus != nil
}

// beginReshard — shadow 이름 확정(SSOT)·진행 status 개시.
func (r *QdrantCollectionReconciler) beginReshard(ctx context.Context, col *qdrantv1alpha1.QdrantCollection, info qdrant.CollectionInfo) (ctrl.Result, error) {
	physical := physicalName(col)
	now := metav1.Now()
	col.Status.Reshard = &qdrantv1alpha1.ReshardStatus{
		Phase:             "Preparing",
		SourceCollection:  physical,
		ShadowCollection:  fmt.Sprintf("%s-rs-g%d", physical, col.Generation),
		TargetShardNumber: *col.Spec.ShardNumber,
		TotalPoints:       info.PointsCount,
		StartedAt:         &now,
	}
	col.Status.Phase = phaseResharding
	commonsevents.Emitf(r.Recorder, col, "ReshardStarted",
		"%s: shard %d→%d (shadow %s)", physical, info.ShardNumber, *col.Spec.ShardNumber, col.Status.Reshard.ShadowCollection)
	if err := r.Status().Update(ctx, col); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: time.Second}, nil
}

// failReshard — pre-swap 실패 공통 처리: shadow 폐기(원본 무손상) + backoff 재시도.
func (r *QdrantCollectionReconciler) failReshard(ctx context.Context, col *qdrantv1alpha1.QdrantCollection, qcl qdrant.Client, reason, msg string) (ctrl.Result, error) {
	rs := col.Status.Reshard
	_ = qcl.DeleteCollection(ctx, rs.ShadowCollection) // shadow 는 임시 소유물 — 폐기 안전
	rs.Phase = "Failed"
	rs.Cursor, rs.CopiedPoints = "", 0
	rs.Attempts++
	col.Status.Phase = condDegraded
	meta.SetStatusCondition(&col.Status.Conditions, metav1.Condition{Type: condDegraded, Status: metav1.ConditionTrue, Reason: reason, Message: msg, ObservedGeneration: col.Generation})
	commonsevents.EmitWarningf(r.Recorder, col, reason, "%s", msg)
	if err := r.Status().Update(ctx, col); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: backoff(rs.Attempts)}, nil
}

// reconcileReshard — 진행 중 워크플로 구동(§9.4). 목표 재변경 시 중단 후 재시작.
func (r *QdrantCollectionReconciler) reconcileReshard(ctx context.Context, col *qdrantv1alpha1.QdrantCollection, cluster *qdrantv1alpha1.QdrantCluster, qcl qdrant.Client) (ctrl.Result, error) {
	rs := col.Status.Reshard

	// 목표 재변경 감지 — 현재 워크플로 중단(shadow 폐기) 후 새 목표로 재시작.
	if col.Spec.ShardNumber == nil || *col.Spec.ShardNumber != rs.TargetShardNumber {
		_ = qcl.DeleteCollection(ctx, rs.ShadowCollection)
		col.Status.Reshard = nil
		commonsevents.Emit(r.Recorder, col, "ReshardAborted", "목표 shardNumber 재변경 — 재시작")
		if err := r.Status().Update(ctx, col); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: time.Second}, nil
	}

	switch rs.Phase {
	case "Preparing", "Failed": // Failed 는 backoff 후 재진입(깨끗한 재시작)
		info, err := qcl.GetCollection(ctx, rs.ShadowCollection)
		if err != nil {
			return r.failReshard(ctx, col, qcl, "ReshardFailed", err.Error())
		}
		switch {
		case !info.Exists:
			if err := qcl.CreateCollection(ctx, rs.ShadowCollection, qdrant.CollectionSpec{
				VectorSize: col.Spec.Vectors.Size, Distance: col.Spec.Vectors.Distance,
				ShardNumber: rs.TargetShardNumber, ReplicationFactor: col.Spec.ReplicationFactor,
			}); err != nil {
				return r.failReshard(ctx, col, qcl, "ReshardFailed", err.Error())
			}
		case info.ShardNumber != rs.TargetShardNumber:
			// 동명 이물 — 임의 삭제 금지(소유 불명), 표면화만.
			rs.Phase = "Blocked"
			r.setDegraded(ctx, col, "ShadowConflict", "shadow 이름의 이물 컬렉션 존재 — 수동 정리 필요")
			return ctrl.Result{RequeueAfter: 5 * time.Minute}, nil
		}
		rs.Phase = "Copying"
		rs.Cursor, rs.CopiedPoints = "", 0
		if err := r.Status().Update(ctx, col); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: time.Second}, nil

	case "Copying":
		return r.reshardCopyStep(ctx, col, cluster, qcl, rs)

	case "Swapping":
		alias := col.Spec.Alias
		aliases, err := qcl.ListAliases(ctx)
		if err != nil {
			return r.failReshard(ctx, col, qcl, "ReshardFailed", err.Error())
		}
		var actions []qdrant.AliasAction
		if _, exists := aliases[alias]; exists {
			actions = append(actions, qdrant.AliasAction{DeleteAlias: &qdrant.DeleteAlias{AliasName: alias}})
		}
		actions = append(actions, qdrant.AliasAction{CreateAlias: &qdrant.CreateAlias{AliasName: alias, CollectionName: rs.ShadowCollection}})
		if err := qcl.UpdateAliases(ctx, actions); err != nil {
			return r.failReshard(ctx, col, qcl, "ReshardFailed", "alias swap: "+err.Error())
		}
		after, err := qcl.ListAliases(ctx) // ★ 커밋점 검증
		if err != nil || after[alias] != rs.ShadowCollection {
			return r.failReshard(ctx, col, qcl, "ReshardFailed", "alias swap 검증 실패")
		}
		rs.Phase = "Finalizing"
		if err := r.Status().Update(ctx, col); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: time.Second}, nil

	case "Finalizing":
		// 커밋점 이후 — 롤백 없음. shadow 가 이미 진본.
		source := rs.SourceCollection
		col.Status.ActiveCollection = rs.ShadowCollection
		if col.Spec.OnDelete == qdrantv1alpha1.CollectionDelete {
			if err := qcl.DeleteCollection(ctx, source); err != nil {
				// 원본 처분 재시도만(진본은 이미 shadow).
				commonsevents.EmitWarning(r.Recorder, col, "SourceDisposalRetry", err)
				return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
			}
			commonsevents.Emit(r.Recorder, col, "SourceDeleted", source)
		} else {
			commonsevents.Emit(r.Recorder, col, "SourceRetained", source)
		}
		col.Status.Reshard = nil
		commonsevents.Emitf(r.Recorder, col, "ReshardCompleted",
			"%s → %s (shard %d)", source, col.Status.ActiveCollection, rs.TargetShardNumber)
		return r.setReady(ctx, col, rs.TotalPoints)

	case "Blocked":
		return ctrl.Result{RequeueAfter: 5 * time.Minute}, nil
	}
	return ctrl.Result{}, nil
}

// reshardCopyStep — Copying 단계(§9.4-3): 배치 복사 + cursor 재개 + 델타 가드.
func (r *QdrantCollectionReconciler) reshardCopyStep(ctx context.Context, col *qdrantv1alpha1.QdrantCollection, cluster *qdrantv1alpha1.QdrantCluster, qcl qdrant.Client, rs *qdrantv1alpha1.ReshardStatus) (ctrl.Result, error) {
	// 전역 배타(§2.2): 클러스터 이동/드레인 중이면 백필 유예(비대칭 — 진행 중 연산 우선).
	if r.clusterMoveActive(cluster) {
		return ctrl.Result{RequeueAfter: 15 * time.Second}, nil
	}
	var offset json.RawMessage
	if rs.Cursor != "" {
		offset = json.RawMessage(rs.Cursor)
	}
	points, next, err := qcl.ScrollPoints(ctx, rs.SourceCollection, offset, reshardCopyBatch)
	if err != nil {
		return r.failReshard(ctx, col, qcl, "ReshardFailed", "scroll: "+err.Error())
	}
	if len(points) > 0 {
		if err := qcl.UpsertPoints(ctx, rs.ShadowCollection, points); err != nil {
			return r.failReshard(ctx, col, qcl, "ReshardFailed", "upsert: "+err.Error())
		}
		rs.CopiedPoints += uint64(len(points))
	}
	if next != nil {
		rs.Cursor = string(next)
		if err := r.Status().Update(ctx, col); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: 200 * time.Millisecond}, nil
	}
	// cursor 소진 — 델타 가드: 복사 중 원본 변이 감지 시 안전 실패(shadow 폐기).
	src, err := qcl.GetCollection(ctx, rs.SourceCollection)
	if err != nil {
		return r.failReshard(ctx, col, qcl, "ReshardFailed", err.Error())
	}
	if src.PointsCount != rs.TotalPoints {
		return r.failReshard(ctx, col, qcl, "SourceMutatedDuringReshard",
			fmt.Sprintf("복사 중 원본 변이(%d→%d) — 읽기전용 창에서 재시도 필요", rs.TotalPoints, src.PointsCount))
	}
	rs.Phase = "Swapping"
	if err := r.Status().Update(ctx, col); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: time.Second}, nil
}
