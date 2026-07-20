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
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	controllerutil "sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	qdrantv1alpha1 "github.com/keiailab/qdrant-operator/api/v1alpha1"
	resources "github.com/keiailab/qdrant-operator/internal/resources"
)

// QdrantClusterReconciler reconciles a QdrantCluster object
type QdrantClusterReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=qdrant.keiailab.com,resources=qdrantclusters,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=qdrant.keiailab.com,resources=qdrantclusters/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=services;configmaps;serviceaccounts,verbs=get;list;watch;create;update;patch;delete

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
//
// QdrantCluster 하나당 5개 child(ServiceAccount/ConfigMap/headless Service/client Service/
// StatefulSet)를 CreateOrUpdate 하고 QdrantCluster를 controller owner로 설정한다. PVC는
// StatefulSet의 volumeClaimTemplates 안에서만 생성되며 별도로 소유하지 않는다
// (CR 삭제 시에도 데이터 잔존 — 설계 §7 데이터 안전).
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.24.1/pkg/reconcile
func (r *QdrantClusterReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	qc := &qdrantv1alpha1.QdrantCluster{}
	if err := r.Get(ctx, req.NamespacedName, qc); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// child 렌더 (PVC는 STS의 volumeClaimTemplates로, 별도 미소유)
	sa := resources.BuildServiceAccount(qc)
	cm := resources.BuildConfigMap(qc)
	hsvc := resources.BuildHeadlessService(qc)
	csvc := resources.BuildClientService(qc)
	sts := resources.BuildStatefulSet(qc)

	for _, obj := range []client.Object{sa, cm, hsvc, csvc, sts} {
		if err := r.applyOwned(ctx, qc, obj); err != nil {
			return ctrl.Result{}, err
		}
	}
	return r.reconcileStatus(ctx, qc, sts)
}

// applyOwned는 desired에 qc를 controller owner로 세팅한 뒤, 기존 리소스가 없으면 Create,
// 있으면 ResourceVersion을 채워 Update한다 — CreateOrUpdate 형태의 멱등 apply.
//
// r.Get은 매니저 기본 캐시 클라이언트라 방금 Create된 리소스를 잠시 NotFound로 볼 수 있다
// (informer 전파 지연 — controller-runtime 공지의 캐시 read-after-write 특성). 이 상태에서
// Create를 시도하면 409 AlreadyExists가 날 수 있는데, 이 경우 최신본을 다시 읽어 Update
// 경로로 수렴시켜 idempotent를 보장한다.
func (r *QdrantClusterReconciler) applyOwned(ctx context.Context, qc *qdrantv1alpha1.QdrantCluster, desired client.Object) error {
	if err := controllerutil.SetControllerReference(qc, desired, r.Scheme); err != nil {
		return err
	}
	existing := desired.DeepCopyObject().(client.Object)
	err := r.Get(ctx, client.ObjectKeyFromObject(desired), existing)
	if apierrors.IsNotFound(err) {
		err = r.Create(ctx, desired)
		if err == nil {
			return nil
		}
		if !apierrors.IsAlreadyExists(err) {
			return err
		}
		err = r.Get(ctx, client.ObjectKeyFromObject(desired), existing)
	}
	if err != nil {
		return err
	}
	desired.SetResourceVersion(existing.GetResourceVersion())
	return r.Update(ctx, desired)
}

// reconcileStatus는 방금 applyOwned로 apply한 STS를 다시 Get해(캐시 read-after-write 지연 —
// applyOwned 주석 참고 — 이 sts 파라미터 자체는 아직 최신 status를 반영하지 못했을 수 있음)
// live status로 QdrantCluster.status를 갱신한다. replicas 전원이 준비돼야만 Running/Ready이고,
// 그 외에는 Provisioning/Progressing이다(Degraded·Scaling·immutable-drift는 Task 9~10에서 추가).
func (r *QdrantClusterReconciler) reconcileStatus(ctx context.Context, qc *qdrantv1alpha1.QdrantCluster, sts *appsv1.StatefulSet) (ctrl.Result, error) {
	live := &appsv1.StatefulSet{}
	if err := r.Get(ctx, client.ObjectKeyFromObject(sts), live); err != nil {
		return ctrl.Result{}, err
	}
	qc.Status.Replicas = live.Status.Replicas
	qc.Status.ReadyReplicas = live.Status.ReadyReplicas
	qc.Status.ObservedGeneration = qc.Generation

	ready := live.Status.ReadyReplicas == qc.Spec.Replicas && qc.Spec.Replicas > 0
	if ready {
		qc.Status.Phase = "Running"
		meta.SetStatusCondition(&qc.Status.Conditions, metav1.Condition{Type: "Ready", Status: metav1.ConditionTrue, Reason: "AllReplicasReady", Message: "모든 replica 준비됨", ObservedGeneration: qc.Generation})
		meta.SetStatusCondition(&qc.Status.Conditions, metav1.Condition{Type: "Progressing", Status: metav1.ConditionFalse, Reason: "Stable", Message: "안정 상태", ObservedGeneration: qc.Generation})
	} else {
		qc.Status.Phase = "Provisioning"
		meta.SetStatusCondition(&qc.Status.Conditions, metav1.Condition{Type: "Progressing", Status: metav1.ConditionTrue, Reason: "Provisioning", Message: "child 리소스 조정 중", ObservedGeneration: qc.Generation})
	}
	if err := r.Status().Update(ctx, qc); err != nil {
		return ctrl.Result{}, err
	}
	if !ready {
		return ctrl.Result{RequeueAfter: 15 * time.Second}, nil
	}
	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *QdrantClusterReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&qdrantv1alpha1.QdrantCluster{}).
		Owns(&appsv1.StatefulSet{}).
		Owns(&corev1.Service{}).
		Owns(&corev1.ConfigMap{}).
		Owns(&corev1.ServiceAccount{}).
		Complete(r)
}
