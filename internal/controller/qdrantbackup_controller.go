/*
Copyright 2026 Keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package controller

import (
	"context"
	"fmt"
	"time"

	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	commonsevents "github.com/keiailab/keiailab-commons/pkg/events"
	qdrantv1alpha1 "github.com/keiailab/qdrant-operator/api/v1alpha1"
	"github.com/keiailab/qdrant-operator/internal/qdrant"
)

// QdrantBackupReconciler 는 스냅샷 백업 세대를 수렴시킨다.
//
// 백업은 만들기보다 **지우지 않기**가 어렵다. 이 컨트롤러가 파괴적으로 행동하는 유일한
// 자리는 retention 이고, 그것은 명시 선언에서만 켜진다.
type QdrantBackupReconciler struct {
	// APIReader 는 캐시를 거치지 않는 읽기다. TLS CA 를 담은 Secret 을 읽는 데만 쓴다 —
	// Secret 을 매니저 캐시에 올리면 범위 안 모든 Secret 을 들고 있게 되고, 이 컨트롤러의
	// 메모리 상한은 128Mi 다.
	APIReader client.Reader
	client.Client
	Scheme   *runtime.Scheme
	Recorder events.EventRecorder
	// QdrantClientFor 는 클러스터 전체(client Service) 대상 — 컬렉션 목록 조회용.
	QdrantClientFor func(cluster *qdrantv1alpha1.QdrantCluster) qdrant.Client
	// QdrantClientForPeer 는 peer 직결 — 스냅샷이 노드 단위라 반드시 이쪽으로 발행한다.
	QdrantClientForPeer func(cluster *qdrantv1alpha1.QdrantCluster, ordinal int32) qdrant.Client
	// Now 는 테스트가 시간을 고정하기 위한 자리다(비면 time.Now).
	Now func() time.Time
}

// +kubebuilder:rbac:groups=qdrant.keiailab.com,resources=qdrantbackups,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=qdrant.keiailab.com,resources=qdrantbackups/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=qdrant.keiailab.com,resources=qdrantbackups/finalizers,verbs=update

func (r *QdrantBackupReconciler) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *QdrantBackupReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	bk := &qdrantv1alpha1.QdrantBackup{}
	if err := r.Get(ctx, req.NamespacedName, bk); err != nil {
		if apierrors.IsNotFound(err) {
			forgetBackup(req.Namespace, req.Name)
		}
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	statusBefore := *bk.Status.DeepCopy()

	cluster := &qdrantv1alpha1.QdrantCluster{}
	if err := r.Get(ctx, types.NamespacedName{Name: bk.Spec.ClusterRef, Namespace: bk.Namespace}, cluster); err != nil {
		if apierrors.IsNotFound(err) {
			r.degrade(bk, "ClusterNotFound",
				fmt.Sprintf("QdrantCluster %q 를 네임스페이스 %s 에서 찾을 수 없음", bk.Spec.ClusterRef, bk.Namespace))
			return r.commit(ctx, bk, statusBefore, 30*time.Second)
		}
		return ctrl.Result{}, err
	}

	// 잘못된 cron 은 조용히 안 도는 백업이 된다 — 가장 나쁜 실패다. 먼저 표면화한다.
	if bk.Spec.Schedule != "" {
		if _, ok := nextRun(bk.Spec.Schedule, r.now()); !ok {
			r.degrade(bk, reasonScheduleValid, fmt.Sprintf("cron 표기를 해석할 수 없음: %q", bk.Spec.Schedule))
			return r.commit(ctx, bk, statusBefore, 0)
		}
	}

	requeue, err := r.advance(ctx, bk, cluster)
	if err != nil {
		return ctrl.Result{}, err
	}
	return r.commit(ctx, bk, statusBefore, requeue)
}

// advance 는 세대를 한 걸음 전진시킨다 — 정산 → 발행 → (비었으면) 마감·예약.
func (r *QdrantBackupReconciler) advance(ctx context.Context, bk *qdrantv1alpha1.QdrantBackup, cluster *qdrantv1alpha1.QdrantCluster) (time.Duration, error) {
	if bk.Status.Active != nil {
		return r.settle(ctx, bk, cluster)
	}
	if len(bk.Status.Pending) > 0 {
		return r.issue(ctx, bk, cluster)
	}
	return r.idle(ctx, bk, cluster)
}

// settle 은 발행한 스냅샷이 목록에 나타났는지 본다. 완료 판정은 발행 응답이 아니라
// 관측이다(wait=false 응답에는 이름이 없다).
func (r *QdrantBackupReconciler) settle(ctx context.Context, bk *qdrantv1alpha1.QdrantBackup, cluster *qdrantv1alpha1.QdrantCluster) (time.Duration, error) {
	active := bk.Status.Active
	qcl := r.QdrantClientForPeer(cluster, active.Peer)

	list, err := qcl.ListSnapshots(ctx, active.Collection)
	if err != nil {
		return 30 * time.Second, nil // 관측 실패는 판정하지 않는다 — 다시 본다
	}

	if snap, ok := newSnapshotName(active.Known, list); ok {
		bk.Status.Snapshots = append(bk.Status.Snapshots, qdrantv1alpha1.SnapshotRef{
			Collection: active.Collection, Peer: active.Peer,
			Name: snap.Name, CreatedAt: snap.CreationTime,
		})
		bk.Status.Active = nil
		return time.Second, nil
	}

	// 기한 안이면 그냥 기다린다. 큰 컬렉션의 스냅샷은 원래 오래 걸린다.
	if active.IssuedAt != nil && time.Since(active.IssuedAt.Time) < snapshotAppearDeadline {
		bk.Status.Phase = phaseBackupCreating
		return 30 * time.Second, nil
	}

	// 기한 초과 — 세대를 버리고 표면화한다. 반쯤 만들어진 세대를 성공으로 기록하지 않는다.
	msg := fmt.Sprintf("%s@peer%d 스냅샷이 %v 안에 나타나지 않음", active.Collection, active.Peer, snapshotAppearDeadline)
	r.degrade(bk, reasonSnapshotFailed, msg)
	commonsevents.EmitWarningf(r.Recorder, bk, reasonSnapshotFailed, "%s", msg)
	bk.Status.Active, bk.Status.Pending = nil, nil
	return time.Minute, nil
}

// issue 는 남은 작업 하나를 발행한다 — 동시 1건.
func (r *QdrantBackupReconciler) issue(ctx context.Context, bk *qdrantv1alpha1.QdrantBackup, cluster *qdrantv1alpha1.QdrantCluster) (time.Duration, error) {
	next := bk.Status.Pending[0]
	qcl := r.QdrantClientForPeer(cluster, next.Peer)

	// 발행 **전** 관측을 남긴다. 이것이 새 스냅샷을 가려내는 유일한 기준이다.
	before, err := qcl.ListSnapshots(ctx, next.Collection)
	if err != nil {
		return 30 * time.Second, nil
	}
	for _, s := range before {
		next.Known = append(next.Known, s.Name)
	}

	if err := qcl.CreateSnapshot(ctx, next.Collection); err != nil {
		msg := fmt.Sprintf("%s@peer%d 스냅샷 발행 실패: %v", next.Collection, next.Peer, err)
		r.degrade(bk, reasonSnapshotFailed, msg)
		commonsevents.EmitWarning(r.Recorder, bk, reasonSnapshotFailed, err)
		bk.Status.Pending = nil
		return time.Minute, nil
	}

	issued := metav1.NewTime(r.now())
	next.IssuedAt = &issued
	bk.Status.Active = &next
	bk.Status.Pending = bk.Status.Pending[1:]
	bk.Status.Phase = phaseBackupCreating
	commonsevents.Emit(r.Recorder, bk, "SnapshotIssued", fmt.Sprintf("%s@peer%d", next.Collection, next.Peer))
	return 10 * time.Second, nil
}

// idle 은 세대 사이의 상태다 — 방금 끝난 세대를 마감하고, 다음 세대를 열지 판단한다.
func (r *QdrantBackupReconciler) idle(ctx context.Context, bk *qdrantv1alpha1.QdrantBackup, cluster *qdrantv1alpha1.QdrantCluster) (time.Duration, error) {
	now := r.now()

	// 직전 걸음에서 세대가 막 끝났다면(산출물은 있는데 마감 시각이 아직 없거나 낡았다면) 마감한다.
	if len(bk.Status.Snapshots) > 0 && r.generationJustFinished(bk) {
		done := metav1.NewTime(now)
		bk.Status.LastSuccessTime = &done
		meta.SetStatusCondition(&bk.Status.Conditions, metav1.Condition{
			Type: condReady, Status: metav1.ConditionTrue, Reason: reasonBackupDone,
			Message:            fmt.Sprintf("스냅샷 %d건 생성", len(bk.Status.Snapshots)),
			ObservedGeneration: bk.Generation,
		})
		r.clearDegraded(bk)
		backupLastSuccess.WithLabelValues(bk.Namespace, bk.Name).Set(float64(done.Unix()))
		backupSnapshots.WithLabelValues(bk.Namespace, bk.Name).Set(float64(len(bk.Status.Snapshots)))
		commonsevents.Emit(r.Recorder, bk, reasonBackupDone, fmt.Sprintf("스냅샷 %d건", len(bk.Status.Snapshots)))

		r.prune(ctx, bk, cluster)
	}

	bk.Status.ObservedGeneration = bk.Generation
	setNextSchedule(bk, now)

	if oneShotDue(bk) || dueNow(bk, now) {
		return r.openGeneration(ctx, bk, cluster)
	}

	bk.Status.Phase = phaseBackupReady
	if bk.Status.LastSuccessTime == nil {
		bk.Status.Phase = phaseBackupIdle
	}

	// 예약이 있으면 그 시각에 깨어난다(상한 5분 — 예약 변경을 오래 놓치지 않기 위해).
	if bk.Status.NextScheduleTime != nil {
		if wait := time.Until(bk.Status.NextScheduleTime.Time); wait > 0 {
			return min(wait, 5*time.Minute), nil
		}
	}
	return 5 * time.Minute, nil
}

// generationJustFinished 는 직전 걸음에서 세대가 막 끝났는지다.
//
// 판정은 phase 로 한다: Creating 이었는데 lane 도 큐도 비었다면 마지막 스냅샷이 방금
// 정산된 것이다. 마감하면서 phase 가 Ready 로 바뀌므로 다음 회차엔 참이 아니다 —
// 같은 세대를 두 번 마감하지 않는다.
func (r *QdrantBackupReconciler) generationJustFinished(bk *qdrantv1alpha1.QdrantBackup) bool {
	return bk.Status.Phase == phaseBackupCreating &&
		bk.Status.Active == nil && len(bk.Status.Pending) == 0
}

// openGeneration 은 새 세대의 작업 목록을 만든다.
func (r *QdrantBackupReconciler) openGeneration(ctx context.Context, bk *qdrantv1alpha1.QdrantBackup, cluster *qdrantv1alpha1.QdrantCluster) (time.Duration, error) {
	collections := bk.Spec.Collections
	if len(collections) == 0 {
		list, err := r.QdrantClientFor(cluster).ListCollections(ctx)
		if err != nil {
			r.degrade(bk, "QdrantUnreachable", err.Error())
			return 30 * time.Second, nil
		}
		collections = list
	}

	plan := planGeneration(collections, cluster.Spec.Replicas)
	if len(plan) == 0 {
		// 백업할 것이 없다 — 실패가 아니다. 빈 세대를 성공으로 기록해 예약만 전진시킨다.
		//
		// 지표도 같이 갱신해야 한다. 여기서 빠뜨리면 status 는 성공인데 마지막 성공 시각이
		// 낡은 채로 남아 "백업이 멈췄다" 알럿이 울린다. 대신 snapshots=0 이 뜨고, 그것이
		// 정확한 신호다 — 백업은 돌았는데 담은 것이 없다.
		done := metav1.NewTime(r.now())
		bk.Status.LastSuccessTime = &done
		bk.Status.Phase = phaseBackupReady
		backupLastSuccess.WithLabelValues(bk.Namespace, bk.Name).Set(float64(done.Unix()))
		backupSnapshots.WithLabelValues(bk.Namespace, bk.Name).Set(0)
		return 5 * time.Minute, nil
	}

	bk.Status.Snapshots = nil // 세대 산출물은 세대마다 새로 쓴다
	bk.Status.Pending = plan
	bk.Status.Phase = phaseBackupCreating
	return time.Second, nil
}

// prune 은 보존기간을 넘긴 스냅샷을 지운다 — 이 컨트롤러의 유일한 파괴 동작이다.
// retention 미지정이면 아무것도 하지 않는다.
func (r *QdrantBackupReconciler) prune(ctx context.Context, bk *qdrantv1alpha1.QdrantBackup, cluster *qdrantv1alpha1.QdrantCluster) {
	if bk.Spec.Retention == nil || bk.Spec.Retention.KeepLast < 1 {
		return
	}

	// 이번 세대가 건드린 (컬렉션, peer) 짝만 대상이다 — 백업이 다루지 않는 컬렉션의
	// 스냅샷까지 이 CR 이 지우면 선언 범위를 넘는다.
	seen := map[string]bool{}
	for _, s := range bk.Status.Snapshots {
		key := fmt.Sprintf("%s@%d", s.Collection, s.Peer)
		if seen[key] {
			continue
		}
		seen[key] = true

		qcl := r.QdrantClientForPeer(cluster, s.Peer)
		list, err := qcl.ListSnapshots(ctx, s.Collection)
		if err != nil {
			continue // 관측 실패로 지우지 않는다 — 모르면 남긴다
		}

		for _, name := range prunable(list, bk.Spec.Retention.KeepLast) {
			if err := qcl.DeleteSnapshot(ctx, s.Collection, name); err != nil {
				commonsevents.EmitWarning(r.Recorder, bk, "PruneFailed", err)
				continue
			}
			commonsevents.Emit(r.Recorder, bk, "SnapshotPruned", fmt.Sprintf("%s@peer%d %s", s.Collection, s.Peer, name))
		}
	}
}

func (r *QdrantBackupReconciler) degrade(bk *qdrantv1alpha1.QdrantBackup, reason, msg string) {
	bk.Status.Phase = phaseBackupDegraded
	meta.SetStatusCondition(&bk.Status.Conditions, metav1.Condition{
		Type: condDegraded, Status: metav1.ConditionTrue, Reason: reason,
		Message: msg, ObservedGeneration: bk.Generation,
	})
}

// clearDegraded 는 성공한 세대가 이전 실패를 회수한다 — 켤 줄만 알고 끌 줄 모르는 조건은
// 다음 진짜 고장을 가린다.
func (r *QdrantBackupReconciler) clearDegraded(bk *qdrantv1alpha1.QdrantBackup) {
	c := meta.FindStatusCondition(bk.Status.Conditions, condDegraded)
	if c == nil || c.Status != metav1.ConditionTrue {
		return
	}
	meta.SetStatusCondition(&bk.Status.Conditions, metav1.Condition{
		Type: condDegraded, Status: metav1.ConditionFalse, Reason: reasonBackupDone,
		Message: "백업 성공 — 이전 실패 회수", ObservedGeneration: bk.Generation,
	})
}

// commit 은 status 가 실제로 변했을 때만 쓴다 — steady-state 재기록 루프를 막는다.
func (r *QdrantBackupReconciler) commit(ctx context.Context, bk *qdrantv1alpha1.QdrantBackup, before qdrantv1alpha1.QdrantBackupStatus, requeue time.Duration) (ctrl.Result, error) {
	if !apiequality.Semantic.DeepEqual(&before, &bk.Status) {
		if err := r.Status().Update(ctx, bk); err != nil {
			return ctrl.Result{}, err
		}
	}
	return requeueOrNothing(requeue), nil
}

func (r *QdrantBackupReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.APIReader == nil {
		r.APIReader = mgr.GetAPIReader()
	}
	if r.QdrantClientFor == nil {
		// 컬렉션 목록 조회용 — 클러스터 client Service DNS.
		r.QdrantClientFor = func(cluster *qdrantv1alpha1.QdrantCluster) qdrant.Client {
			return newClusterClient(context.Background(), r.APIReader, cluster, clientBaseURL(cluster))
		}
	}
	if r.QdrantClientForPeer == nil {
		// 스냅샷은 노드 단위라 발행·관측·삭제 전부 peer 직결(headless 파드 DNS)이어야 한다.
		// client Service 로 보내면 어느 peer 가 받을지 알 수 없어 세대가 뒤섞인다.
		r.QdrantClientForPeer = func(cluster *qdrantv1alpha1.QdrantCluster, ordinal int32) qdrant.Client {
			return newClusterClient(context.Background(), r.APIReader, cluster, peerBaseURL(cluster, ordinal))
		}
	}
	r.Recorder = mgr.GetEventRecorder("qdrantbackup")

	return ctrl.NewControllerManagedBy(mgr).
		For(&qdrantv1alpha1.QdrantBackup{}).
		Named("qdrantbackup").
		Complete(r)
}
