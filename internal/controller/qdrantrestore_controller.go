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
	"github.com/keiailab/qdrant-operator/internal/resources"
)

const (
	phaseRestoring       = "Restoring"
	phaseRestoreDone     = "Ready"
	reasonRestoreDone    = "RestoreCompleted"
	reasonRestoreFailed  = "RestoreFailed"
	reasonNoSnapshotRefs = "NoSnapshotSources"
)

// QdrantRestoreReconciler 는 스냅샷 복원을 1회 수행한다.
//
// 완료된 복원은 다시 돌지 않는다 — CR 이 남아 있다는 이유로 재복원하면 그 뒤에 들어온
// 데이터를 조용히 되돌린다. 다시 하려면 CR 을 새로 만든다.
type QdrantRestoreReconciler struct {
	client.Client
	Scheme              *runtime.Scheme
	Recorder            events.EventRecorder
	QdrantClientForPeer func(cluster *qdrantv1alpha1.QdrantCluster, ordinal int32) qdrant.Client
}

// +kubebuilder:rbac:groups=qdrant.keiailab.com,resources=qdrantrestores,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=qdrant.keiailab.com,resources=qdrantrestores/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=qdrant.keiailab.com,resources=qdrantrestores/finalizers,verbs=update

func (r *QdrantRestoreReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	rs := &qdrantv1alpha1.QdrantRestore{}
	if err := r.Get(ctx, req.NamespacedName, rs); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// 이미 끝났으면 끝이다.
	if rs.Status.CompletionTime != nil {
		return ctrl.Result{}, nil
	}
	statusBefore := *rs.Status.DeepCopy()

	cluster := &qdrantv1alpha1.QdrantCluster{}
	if err := r.Get(ctx, types.NamespacedName{Name: rs.Spec.ClusterRef, Namespace: rs.Namespace}, cluster); err != nil {
		if apierrors.IsNotFound(err) {
			r.degradeRestore(rs, "ClusterNotFound",
				fmt.Sprintf("QdrantCluster %q 를 네임스페이스 %s 에서 찾을 수 없음", rs.Spec.ClusterRef, rs.Namespace))
			return r.commitRestore(ctx, rs, statusBefore, 30*time.Second)
		}
		return ctrl.Result{}, err
	}

	// 좌표가 아직 없으면 만든다(1회).
	if len(rs.Status.Pending) == 0 && len(rs.Status.Restored) == 0 {
		sources, err := r.resolveSources(ctx, rs, cluster)
		if err != nil {
			r.degradeRestore(rs, reasonNoSnapshotRefs, err.Error())
			return r.commitRestore(ctx, rs, statusBefore, time.Minute)
		}
		rs.Status.Pending = sources
		rs.Status.Phase = phaseRestoring
	}

	if len(rs.Status.Pending) > 0 {
		return r.restoreNext(ctx, rs, cluster, statusBefore)
	}

	done := metav1.Now()
	rs.Status.CompletionTime = &done
	rs.Status.Phase = phaseRestoreDone
	rs.Status.ObservedGeneration = rs.Generation
	meta.SetStatusCondition(&rs.Status.Conditions, metav1.Condition{
		Type: condReady, Status: metav1.ConditionTrue, Reason: reasonRestoreDone,
		Message:            fmt.Sprintf("peer %d대 복원 발행", len(rs.Status.Restored)),
		ObservedGeneration: rs.Generation,
	})
	commonsevents.Emit(r.Recorder, rs, reasonRestoreDone, rs.Spec.Collection)
	return r.commitRestore(ctx, rs, statusBefore, 0)
}

// resolveSources 는 복원 좌표를 정한다 — 명시 Sources 가 우선, 없으면 백업 CR 에서 끌어온다.
func (r *QdrantRestoreReconciler) resolveSources(ctx context.Context, rs *qdrantv1alpha1.QdrantRestore, cluster *qdrantv1alpha1.QdrantCluster) ([]qdrantv1alpha1.RestoreSource, error) {
	if len(rs.Spec.Sources) > 0 {
		return rs.Spec.Sources, nil
	}
	if rs.Spec.FromBackup == "" {
		return nil, fmt.Errorf("sources 또는 fromBackup 중 하나가 필요하다")
	}

	// S3 보관이면 peer 가 자기 스냅샷을 HTTP 로 서빙하지 않는다. 여기서 주소를 지어내면
	// 존재하지 않는 URL 로 복원을 발행하게 되므로, 모른다고 말하고 멈춘다.
	if sn := cluster.Spec.Snapshots; sn != nil && sn.Storage == qdrantv1alpha1.SnapshotStorageS3 {
		return nil, fmt.Errorf("스냅샷이 S3 에 있어 peer 주소를 유추할 수 없다 — spec.sources 로 위치를 지정한다")
	}

	backup := &qdrantv1alpha1.QdrantBackup{}
	if err := r.Get(ctx, types.NamespacedName{Name: rs.Spec.FromBackup, Namespace: rs.Namespace}, backup); err != nil {
		return nil, fmt.Errorf("QdrantBackup %q 를 읽을 수 없다: %w", rs.Spec.FromBackup, err)
	}

	var out []qdrantv1alpha1.RestoreSource
	for _, snap := range backup.Status.Snapshots {
		if snap.Collection != rs.Spec.Collection {
			continue
		}
		out = append(out, qdrantv1alpha1.RestoreSource{
			Peer:     snap.Peer,
			Location: peerSnapshotURL(cluster, snap.Peer, snap.Collection, snap.Name),
		})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("QdrantBackup %q 에 컬렉션 %q 의 스냅샷이 없다", rs.Spec.FromBackup, rs.Spec.Collection)
	}
	return out, nil
}

// peerSnapshotURL 은 그 peer 자신이 보관 중인 스냅샷의 다운로드 주소다. 복원을 같은 peer 에
// 발행하므로 자기 자신을 읽는 셈이고, 그래서 클러스터 밖 도달성이 필요 없다.
func peerSnapshotURL(cluster *qdrantv1alpha1.QdrantCluster, ordinal int32, collection, snapshot string) string {
	return fmt.Sprintf("http://%s-%d.%s.%s.svc:%d/collections/%s/snapshots/%s",
		cluster.Name, ordinal, resources.HeadlessName(cluster), cluster.Namespace,
		resources.RESTPort, collection, snapshot)
}

// restoreNext 는 남은 peer 하나에 복원을 발행한다 — 동시 1건.
func (r *QdrantRestoreReconciler) restoreNext(ctx context.Context, rs *qdrantv1alpha1.QdrantRestore, cluster *qdrantv1alpha1.QdrantCluster, before qdrantv1alpha1.QdrantRestoreStatus) (ctrl.Result, error) {
	next := rs.Status.Pending[0]

	priority := rs.Spec.Priority
	if priority == "" {
		priority = qdrant.SnapshotPrioritySnapshot
	}

	qcl := r.QdrantClientForPeer(cluster, next.Peer)
	if err := qcl.RecoverSnapshot(ctx, rs.Spec.Collection, next.Location, priority); err != nil {
		msg := fmt.Sprintf("peer%d 복원 실패: %v", next.Peer, err)
		r.degradeRestore(rs, reasonRestoreFailed, msg)
		commonsevents.EmitWarning(r.Recorder, rs, reasonRestoreFailed, err)
		return r.commitRestore(ctx, rs, before, time.Minute)
	}

	rs.Status.Restored = append(rs.Status.Restored, next)
	rs.Status.Pending = rs.Status.Pending[1:]
	rs.Status.Phase = phaseRestoring
	commonsevents.Emit(r.Recorder, rs, "RestoreIssued", fmt.Sprintf("%s@peer%d", rs.Spec.Collection, next.Peer))
	return r.commitRestore(ctx, rs, before, time.Second)
}

func (r *QdrantRestoreReconciler) degradeRestore(rs *qdrantv1alpha1.QdrantRestore, reason, msg string) {
	rs.Status.Phase = phaseBackupDegraded
	meta.SetStatusCondition(&rs.Status.Conditions, metav1.Condition{
		Type: condDegraded, Status: metav1.ConditionTrue, Reason: reason,
		Message: msg, ObservedGeneration: rs.Generation,
	})
}

func (r *QdrantRestoreReconciler) commitRestore(ctx context.Context, rs *qdrantv1alpha1.QdrantRestore, before qdrantv1alpha1.QdrantRestoreStatus, requeue time.Duration) (ctrl.Result, error) {
	if !apiequality.Semantic.DeepEqual(&before, &rs.Status) {
		if err := r.Status().Update(ctx, rs); err != nil {
			return ctrl.Result{}, err
		}
	}
	return requeueOrNothing(requeue), nil
}

func (r *QdrantRestoreReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.QdrantClientForPeer == nil {
		r.QdrantClientForPeer = func(cluster *qdrantv1alpha1.QdrantCluster, ordinal int32) qdrant.Client {
			return qdrant.NewHTTPClient(fmt.Sprintf("http://%s-%d.%s.%s.svc:%d",
				cluster.Name, ordinal, resources.HeadlessName(cluster), cluster.Namespace, resources.RESTPort))
		}
	}
	r.Recorder = mgr.GetEventRecorder("qdrantrestore")

	return ctrl.NewControllerManagedBy(mgr).
		For(&qdrantv1alpha1.QdrantRestore{}).
		Named("qdrantrestore").
		Complete(r)
}
