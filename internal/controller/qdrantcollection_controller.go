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
	// 물리/논리 분리(§9.2): ensure/adopt/삭제/alias 는 모두 physical(activeCollection)을 키로.
	name := physicalName(col)

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

	// 진행 중 리샤드 워크플로 최우선 구동(§9.4).
	if col.Status.Reshard != nil {
		return r.reconcileReshard(ctx, col, cluster, qc)
	}

	info, err := qc.GetCollection(ctx, name)
	if err != nil {
		r.setDegraded(ctx, col, "QdrantUnreachable", err.Error())
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	// ShardNumber nil = "라이브 값 채택"(3중 opt-in 의 1겹) — 불일치·리샤드 트리거 없음.
	desired := qdrant.CollectionSpec{
		VectorSize:        col.Spec.Vectors.Size,
		Distance:          col.Spec.Vectors.Distance,
		ShardNumber:       resolvedShardNumber(col, info),
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
		col.Status.ActiveCollection = name
		if err := r.ensureAlias(ctx, qc, col.Spec.Alias, name); err != nil {
			r.setDegraded(ctx, col, "AliasFailed", err.Error())
			return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
		}
		return r.setReady(ctx, col, 0)

	case paramsMatch(info, desired):
		// 존재 + 파라미터 일치 → 채택. CR 생성으로 만들어진 경우가 아니면 adopted 로 표시.
		if !meta.IsStatusConditionTrue(col.Status.Conditions, condReady) {
			col.Status.Adopted = true
			r.Recorder.Event(col, "Normal", "CollectionAdopted", name)
		}
		col.Status.ActiveCollection = name
		if err := r.ensureAlias(ctx, qc, col.Spec.Alias, name); err != nil {
			r.setDegraded(ctx, col, "AliasFailed", err.Error())
			return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
		}
		return r.setReady(ctx, col, info.PointsCount)

	case reshardable(info, col) && col.Spec.Reshard == qdrantv1alpha1.ReshardAuto && col.Spec.Alias != "":
		// 3중 opt-in 충족(§9.1) — 단 전역 배타: 클러스터 이동/드레인 중이면 시작 유예(§2.2).
		if r.clusterMoveActive(cluster) {
			return ctrl.Result{RequeueAfter: 15 * time.Second}, nil
		}
		return r.beginReshard(ctx, col, info)

	case reshardable(info, col):
		// shardNumber 단독 상이 — 해소 가능(리샤드 opt-in 안내), ParamsMismatch 와 상호배타.
		r.setDegraded(ctx, col, "ReshardRequired",
			fmt.Sprintf("shardNumber 상이(live %d / spec %d) — spec.alias 설정 + spec.reshard=Auto 로 opt-in 시 무중단 리샤드", info.ShardNumber, desired.ShardNumber))
		return ctrl.Result{RequeueAfter: 5 * time.Minute}, nil

	default:
		// 파라미터 불일치 — 파괴적 재생성은 절대 하지 않고 Degraded 로만 알린다.
		r.setDegraded(ctx, col, "ParamsMismatch", fmt.Sprintf(
			"라이브 컬렉션 파라미터가 spec 과 다름 (live: size=%d distance=%s shards=%d repl=%d / spec: size=%d distance=%s shards=%d repl=%d) — 자동 재생성 금지",
			info.VectorSize, info.Distance, info.ShardNumber, info.ReplicationFactor,
			desired.VectorSize, desired.Distance, desired.ShardNumber, desired.ReplicationFactor))
		return ctrl.Result{RequeueAfter: 5 * time.Minute}, nil
	}
}

// paramsMatch 는 CR 이 관리하는 파라미터 부분집합만 대조한다.
//
// RF 만 예외적으로 "승격 허용" 비교다(live <= want): qdrant 는 기존 컬렉션의
// replication_factor 를 바꿀 API 가 없고(PATCH 는 result:true 를 주면서 무시 — v1.18
// 실측), 실질 내구성은 shard replica 수로 결정된다. 따라서 spec 이 라이브보다 큰 RF 를
// 요구하면 mismatch(Degraded) 가 아니라 **채택 + 목표 상향**으로 처리하고, 실제 수렴은
// 클러스터 컨트롤러의 자동 재복제(planReplications)가 담당한다. 축소(live > want)는
// 여전히 불일치 — 자동 replica 제거는 파괴 동작이라 하지 않는다.
func paramsMatch(live qdrant.CollectionInfo, want qdrant.CollectionSpec) bool {
	return live.VectorSize == want.VectorSize &&
		live.Distance == want.Distance &&
		live.ShardNumber == want.ShardNumber &&
		live.ReplicationFactor <= want.ReplicationFactor
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
