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
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	qdrantv1alpha1 "github.com/keiailab/qdrant-operator/api/v1alpha1"
	"github.com/keiailab/qdrant-operator/internal/qdrant"
	"github.com/keiailab/qdrant-operator/internal/resources"
)

// collectionFinalizer 는 onDelete=Delete 인 CR 에만 부착 — Retain(기본)은 파이널라이저
// 자체를 두지 않아 CR 삭제가 데이터에 어떤 영향도 못 준다(설계 안전 원칙).
const collectionFinalizer = "qdrant.keiailab.com/collection-cleanup"

// QdrantCollectionReconciler reconciles a QdrantCollection object
type QdrantCollectionReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
	// QdrantClientFor 는 대상 QdrantCluster 의 REST 엔드포인트 클라이언트를 만든다.
	// 프로덕션은 client Service DNS 기반 HTTP, envtest 는 Fake 를 주입한다.
	QdrantClientFor func(cluster *qdrantv1alpha1.QdrantCluster) qdrant.Client
}

// +kubebuilder:rbac:groups=qdrant.keiailab.com,resources=qdrantcollections,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=qdrant.keiailab.com,resources=qdrantcollections/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=qdrant.keiailab.com,resources=qdrantcollections/finalizers,verbs=update

func (r *QdrantCollectionReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	col := &qdrantv1alpha1.QdrantCollection{}
	if err := r.Get(ctx, req.NamespacedName, col); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// 대상 클러스터 확인 — 없으면 Degraded 로 표면화 후 재시도.
	cluster := &qdrantv1alpha1.QdrantCluster{}
	if err := r.Get(ctx, types.NamespacedName{Name: col.Spec.ClusterRef, Namespace: col.Namespace}, cluster); err != nil {
		if apierrors.IsNotFound(err) {
			r.setDegraded(ctx, col, "ClusterNotFound",
				fmt.Sprintf("QdrantCluster %q 를 네임스페이스 %s 에서 찾을 수 없음", col.Spec.ClusterRef, col.Namespace))
			return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
		}
		return ctrl.Result{}, err
	}
	qc := r.QdrantClientFor(cluster)
	name := col.TargetCollectionName()

	// 삭제 경로 — onDelete=Delete 로 부착된 파이널라이저만 처리한다.
	if !col.DeletionTimestamp.IsZero() {
		if controllerutil.ContainsFinalizer(col, collectionFinalizer) {
			if err := qc.DeleteCollection(ctx, name); err != nil {
				r.Recorder.Event(col, "Warning", "DeleteFailed", err.Error())
				return ctrl.Result{}, err
			}
			r.Recorder.Event(col, "Normal", "CollectionDeleted", name)
			controllerutil.RemoveFinalizer(col, collectionFinalizer)
			if err := r.Update(ctx, col); err != nil {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{}, nil
	}

	// 파이널라이저는 Delete 정책일 때만 부착, Retain 으로 바뀌면 제거.
	if col.Spec.OnDelete == qdrantv1alpha1.CollectionDelete {
		if controllerutil.AddFinalizer(col, collectionFinalizer) {
			if err := r.Update(ctx, col); err != nil {
				return ctrl.Result{}, err
			}
		}
	} else if controllerutil.RemoveFinalizer(col, collectionFinalizer) {
		if err := r.Update(ctx, col); err != nil {
			return ctrl.Result{}, err
		}
	}

	info, err := qc.GetCollection(ctx, name)
	if err != nil {
		r.setDegraded(ctx, col, "QdrantUnreachable", err.Error())
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	desired := qdrant.CollectionSpec{
		VectorSize:        col.Spec.Vectors.Size,
		Distance:          col.Spec.Vectors.Distance,
		ShardNumber:       col.Spec.ShardNumber,
		ReplicationFactor: col.Spec.ReplicationFactor,
	}

	switch {
	case !info.Exists:
		if err := qc.CreateCollection(ctx, name, desired); err != nil {
			r.setDegraded(ctx, col, "CreateFailed", err.Error())
			return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
		}
		r.Recorder.Event(col, "Normal", "CollectionCreated", name)
		col.Status.Adopted = false
		return r.setReady(ctx, col, 0)

	case paramsMatch(info, desired):
		// 존재 + 파라미터 일치 → 채택. CR 생성으로 만들어진 경우가 아니면 adopted 로 표시.
		if !meta.IsStatusConditionTrue(col.Status.Conditions, condReady) {
			col.Status.Adopted = true
			r.Recorder.Event(col, "Normal", "CollectionAdopted", name)
		}
		return r.setReady(ctx, col, info.PointsCount)

	default:
		// 파라미터 불일치 — 파괴적 재생성은 절대 하지 않고 Degraded 로만 알린다.
		r.setDegraded(ctx, col, "ParamsMismatch", fmt.Sprintf(
			"라이브 컬렉션 파라미터가 spec 과 다름 (live: size=%d distance=%s shards=%d repl=%d / spec: size=%d distance=%s shards=%d repl=%d) — 자동 재생성 금지, re-shard 워크플로 또는 spec 정합 필요",
			info.VectorSize, info.Distance, info.ShardNumber, info.ReplicationFactor,
			desired.VectorSize, desired.Distance, desired.ShardNumber, desired.ReplicationFactor))
		return ctrl.Result{RequeueAfter: 5 * time.Minute}, nil
	}
}

// paramsMatch 는 CR 이 관리하는 파라미터 부분집합만 대조한다.
func paramsMatch(live qdrant.CollectionInfo, want qdrant.CollectionSpec) bool {
	return live.VectorSize == want.VectorSize &&
		live.Distance == want.Distance &&
		live.ShardNumber == want.ShardNumber &&
		live.ReplicationFactor == want.ReplicationFactor
}

func (r *QdrantCollectionReconciler) setReady(ctx context.Context, col *qdrantv1alpha1.QdrantCollection, points uint64) (ctrl.Result, error) {
	col.Status.Phase = condReady
	col.Status.PointsCount = points
	col.Status.ObservedGeneration = col.Generation
	meta.SetStatusCondition(&col.Status.Conditions, metav1.Condition{
		Type: condReady, Status: metav1.ConditionTrue, Reason: "Ensured",
		Message: "컬렉션 존재 + 파라미터 일치", ObservedGeneration: col.Generation})
	meta.SetStatusCondition(&col.Status.Conditions, metav1.Condition{
		Type: condDegraded, Status: metav1.ConditionFalse, Reason: "Healthy",
		Message: "정상", ObservedGeneration: col.Generation})
	if err := r.Status().Update(ctx, col); err != nil {
		return ctrl.Result{}, err
	}
	// 포인트 수 등 관측값 주기 갱신.
	return ctrl.Result{RequeueAfter: 2 * time.Minute}, nil
}

func (r *QdrantCollectionReconciler) setDegraded(ctx context.Context, col *qdrantv1alpha1.QdrantCollection, reason, msg string) {
	col.Status.Phase = condDegraded
	col.Status.ObservedGeneration = col.Generation
	meta.SetStatusCondition(&col.Status.Conditions, metav1.Condition{
		Type: condDegraded, Status: metav1.ConditionTrue, Reason: reason,
		Message: msg, ObservedGeneration: col.Generation})
	r.Recorder.Event(col, "Warning", reason, msg)
	_ = r.Status().Update(ctx, col)
}

// SetupWithManager sets up the controller with the Manager.
func (r *QdrantCollectionReconciler) SetupWithManager(mgr ctrl.Manager) error {
	r.Recorder = mgr.GetEventRecorderFor("qdrantcollection")
	if r.QdrantClientFor == nil {
		// 프로덕션 기본: 클러스터 client Service DNS (오퍼레이터가 클러스터 안에서 동작 전제).
		r.QdrantClientFor = func(cluster *qdrantv1alpha1.QdrantCluster) qdrant.Client {
			return qdrant.NewHTTPClient(fmt.Sprintf("http://%s.%s.svc:%d",
				resources.ClientName(cluster), cluster.Namespace, resources.RESTPort))
		}
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&qdrantv1alpha1.QdrantCollection{}).
		Named("qdrantcollection").
		Complete(r)
}
