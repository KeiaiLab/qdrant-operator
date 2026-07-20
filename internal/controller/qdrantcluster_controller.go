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
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	controllerutil "sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	qdrantv1alpha1 "github.com/keiailab/qdrant-operator/api/v1alpha1"
	resources "github.com/keiailab/qdrant-operator/internal/resources"
)

// QdrantClusterReconciler reconciles a QdrantCluster object
type QdrantClusterReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
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

	for _, obj := range []client.Object{sa, cm, hsvc, csvc} {
		if err := r.applyOwned(ctx, qc, obj); err != nil {
			return ctrl.Result{}, err
		}
	}

	// STS는 volumeClaimTemplates(스토리지 크기)·serviceName·selector가 immutable이라 apiserver가
	// Update를 거부한다 — apply 직전에 라이브 STS와 비교해 drift가 있으면 (불가능한) patch를 시도하는
	// 대신 Degraded condition + Event로 표면화하고 종료한다 (crash-loop 방지, Task 9). 분산 DB의
	// scale-down(최고 서수 peer + shard 유실 위험)도 같은 자리에서 거부한다 (Task 10) — 두 가드가
	// apply 직전 동일 liveSTS 한 번의 Get을 공유한다.
	liveSTS := &appsv1.StatefulSet{}
	if err := r.Get(ctx, client.ObjectKeyFromObject(sts), liveSTS); err == nil {
		// immutable-drift 가드 (Task 9) — stsImmutableChanged는 ownerRef를 보지 않으므로 sts를
		// 그대로 전달한다 (DeepCopy+SetControllerReference 불필요).
		if stsImmutableChanged(liveSTS, sts) {
			meta.SetStatusCondition(&qc.Status.Conditions, metav1.Condition{Type: "Degraded", Status: metav1.ConditionTrue, Reason: "ImmutableFieldChanged", Message: "STS immutable 필드 변경 감지 — 수동 재생성 필요(Phase A 미지원)", ObservedGeneration: qc.Generation})
			r.Recorder.Event(qc, corev1.EventTypeWarning, "ImmutableFieldChanged", "persistence/serviceName/selector 변경은 Phase A에서 미지원")
			_ = r.Status().Update(ctx, qc)
			return ctrl.Result{}, nil // STS Update 시도 안 함 (crash-loop 방지)
		}
		// scale-down 가드 (Task 10) — 같은 liveSTS 재사용. naive replica 감소는 최고 서수 peer와
		// 그 shard를 유실시키므로(OSS는 자동 reshard 없음) Phase A는 scale-up만 허용한다.
		if liveSTS.Spec.Replicas != nil && qc.Spec.Replicas < *liveSTS.Spec.Replicas {
			meta.SetStatusCondition(&qc.Status.Conditions, metav1.Condition{Type: "Degraded", Status: metav1.ConditionTrue, Reason: "ScaleDownRefused", Message: "분산 DB scale-down은 Phase A 미지원(shard 유실 위험) — Phase B의 안전 drain 필요", ObservedGeneration: qc.Generation})
			r.Recorder.Event(qc, corev1.EventTypeWarning, "ScaleDownRefused", "scale-down 거부됨")
			_ = r.Status().Update(ctx, qc)
			return ctrl.Result{}, nil // STS Update 시도 안 함 (replica 유지)
		}
	}

	if err := r.applyOwned(ctx, qc, sts); err != nil {
		return ctrl.Result{}, err
	}
	return r.reconcileStatus(ctx, qc, sts)
}

// stsImmutableChanged는 라이브 STS(existing)와 렌더 결과(desired) 사이에 apiserver가 거부할
// immutable 필드 변경이 있는지 검사한다.
func stsImmutableChanged(existing, desired *appsv1.StatefulSet) bool {
	if existing.Spec.ServiceName != desired.Spec.ServiceName {
		return true
	}
	// VCT 전체 DeepEqual 은 apiserver 가 채운 default(volumeMode 등) 로 항상 불일치 → 오탐.
	// 실제 immutable 변경 신호인 스토리지 크기 요청만 표적 비교한다.
	if len(existing.Spec.VolumeClaimTemplates) > 0 && len(desired.Spec.VolumeClaimTemplates) > 0 {
		e := existing.Spec.VolumeClaimTemplates[0].Spec.Resources.Requests.Storage()
		d := desired.Spec.VolumeClaimTemplates[0].Spec.Resources.Requests.Storage()
		if !e.Equal(*d) {
			return true
		}
	}
	if !apiequality.Semantic.DeepEqual(existing.Spec.Selector, desired.Spec.Selector) {
		return true
	}
	return false
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
// 그 외에는 Provisioning/Progressing이다(immutable-drift·scale-down Degraded는 Reconcile에서 STS
// apply 전에 먼저 처리되어 여기까지 오지 않음 — Task 9/10).
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
	r.Recorder = mgr.GetEventRecorderFor("qdrantcluster")
	return ctrl.NewControllerManagedBy(mgr).
		For(&qdrantv1alpha1.QdrantCluster{}).
		Owns(&appsv1.StatefulSet{}).
		Owns(&corev1.Service{}).
		Owns(&corev1.ConfigMap{}).
		Owns(&corev1.ServiceAccount{}).
		Complete(r)
}
