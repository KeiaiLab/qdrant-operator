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
	"strconv"
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
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	controllerutil "sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	qdrantv1alpha1 "github.com/keiailab/qdrant-operator/api/v1alpha1"
	"github.com/keiailab/qdrant-operator/internal/qdrant"
	resources "github.com/keiailab/qdrant-operator/internal/resources"
)

// condition 타입 / phase 값 상수 — QdrantCluster·QdrantCollection 컨트롤러 공용.
const (
	condReady       = "Ready"
	condDegraded    = "Degraded"
	condProgressing = "Progressing"
)

// QdrantClusterReconciler reconciles a QdrantCluster object
type QdrantClusterReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
	// QdrantClientFor 는 대상 클러스터의 REST 클라이언트를 만든다(B-2 분포 관측·B-3/B-4 실행).
	// 프로덕션은 client Service DNS 기반 HTTP, envtest 는 Fake 를 주입한다.
	QdrantClientFor func(cluster *qdrantv1alpha1.QdrantCluster) qdrant.Client
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
			// 관측-우선(R1-5): status 정지 방지 — immutable 대기 중에도 B-2 관측은 갱신한다.
			_ = r.observe(ctx, qc)
			meta.SetStatusCondition(&qc.Status.Conditions, metav1.Condition{Type: condDegraded, Status: metav1.ConditionTrue, Reason: "ImmutableFieldChanged", Message: "STS immutable 필드 변경 감지 — 수동 재조정 필요", ObservedGeneration: qc.Generation})
			r.Recorder.Event(qc, corev1.EventTypeWarning, "ImmutableFieldChanged", "persistence/serviceName/selector 변경은 미지원")
			if qc.Status.DrainStatus != nil { // 진행 중 드레인은 '일시중단' 을 명시(무경고 정지 금지)
				qc.Status.DrainStatus.Message = "immutable-drift — STS 수동 재조정 대기, 드레인 일시중단"
				qc.Status.Phase = phaseDraining
			}
			_ = r.Status().Update(ctx, qc)
			return ctrl.Result{}, nil // STS Update 시도 안 함 (crash-loop 방지)
		}
		// B-4 scale-in drain — 구 거부 가드를 안전 절차로 대체. 드레인 미완 구간에는 STS 를
		// apply 하지 않아 현 replicas 가 유지되고(qc.Spec.Replicas 로 렌더된 sts 의 override 함정
		// 회피), 이동/드롭 → RemovePeer(force=false) → 1 서수 축소 순서를 강제한다.
		if liveSTS.Spec.Replicas != nil && qc.Spec.Replicas < *liveSTS.Spec.Replicas {
			return r.reconcileDrainCycle(ctx, qc, liveSTS)
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
	statusBefore := *qc.Status.DeepCopy()
	qc.Status.Replicas = live.Status.Replicas
	qc.Status.ReadyReplicas = live.Status.ReadyReplicas
	qc.Status.ObservedGeneration = qc.Generation
	// 정상(비-축소) 경로 진입 = 드레인 없음 — 정상 완료와 목표 상향 abort 를 이 한 지점이 흡수(R1-6).
	qc.Status.DrainStatus = nil

	// B-2 관측(GET only, best-effort) — 실패해도 reconcile 을 막지 않는다.
	obs := r.observe(ctx, qc)

	ready := live.Status.ReadyReplicas == qc.Spec.Replicas && qc.Spec.Replicas > 0
	var requeue time.Duration
	switch {
	case !ready:
		qc.Status.Phase = "Provisioning"
		meta.SetStatusCondition(&qc.Status.Conditions, metav1.Condition{Type: condProgressing, Status: metav1.ConditionTrue, Reason: "Provisioning", Message: "child 리소스 조정 중", ObservedGeneration: qc.Generation})
		requeue = 15 * time.Second

	case obs == nil || len(obs.Peers) != int(qc.Spec.Replicas):
		// ready 이나 qdrant 관측 실패 또는 peer 합류 미완(scale 직후 raft join 대기) —
		// 계획·집행 없이 관측만 반복한다(B-3 ready 게이트).
		qc.Status.Phase = phaseRunning
		meta.SetStatusCondition(&qc.Status.Conditions, metav1.Condition{Type: condReady, Status: metav1.ConditionTrue, Reason: "AllReplicasReady", Message: "모든 replica 준비됨", ObservedGeneration: qc.Generation})
		meta.SetStatusCondition(&qc.Status.Conditions, metav1.Condition{Type: condProgressing, Status: metav1.ConditionTrue, Reason: "PeersJoining", Message: "qdrant peer 합류/관측 대기", ObservedGeneration: qc.Generation})
		requeue = 15 * time.Second

	default:
		// B-3: ready + peer 완비 — rebalance 스텝이 최종 phase 를 결정한다.
		phase, d := r.reconcileRebalance(ctx, qc, obs, r.QdrantClientFor(qc))
		qc.Status.Phase = phase
		meta.SetStatusCondition(&qc.Status.Conditions, metav1.Condition{Type: condReady, Status: metav1.ConditionTrue, Reason: "AllReplicasReady", Message: "모든 replica 준비됨", ObservedGeneration: qc.Generation})
		if phase == phaseRebalancing {
			meta.SetStatusCondition(&qc.Status.Conditions, metav1.Condition{Type: condProgressing, Status: metav1.ConditionTrue, Reason: "Rebalancing", Message: "shard 재배치 진행 중", ObservedGeneration: qc.Generation})
		} else {
			meta.SetStatusCondition(&qc.Status.Conditions, metav1.Condition{Type: condProgressing, Status: metav1.ConditionFalse, Reason: "Stable", Message: "안정 상태", ObservedGeneration: qc.Generation})
		}
		requeue = d
	}

	// self-trigger 방어(§5.2): status 가 실제로 변했을 때만 커밋 — steady-state 재기록 루프 차단.
	if !apiequality.Semantic.DeepEqual(&statusBefore, &qc.Status) {
		if err := r.Status().Update(ctx, qc); err != nil {
			return ctrl.Result{}, err
		}
	}
	return requeueOrNothing(requeue), nil
}

// observe 는 qdrant 관측(GET only)으로 status.Peers/ShardDistribution 을 채우고(B-2),
// planner/executor 가 소비할 원시 스냅샷을 반환한다. 순수 관측 — 어떤 쓰기도 발행하지
// 않으며, 실패 시 nil 반환(분포 보고는 best-effort, reconcile 진행을 막지 않는다).
func (r *QdrantClusterReconciler) observe(ctx context.Context, qc *qdrantv1alpha1.QdrantCluster) *observation {
	if r.QdrantClientFor == nil {
		return nil
	}
	qcl := r.QdrantClientFor(qc)
	ci, err := qcl.ClusterInfo(ctx)
	if err != nil {
		return nil
	}
	peers := make([]string, 0, len(ci.Peers))
	for _, p := range ci.Peers {
		peers = append(peers, strconv.FormatUint(p.ID, 10))
	}
	qc.Status.Peers = peers

	names, err := qcl.ListCollections(ctx)
	if err != nil {
		return nil
	}
	obs := &observation{Peers: ci.Peers, Collections: map[string]*qdrant.CollectionClusterInfo{}}
	dist := make([]qdrantv1alpha1.CollectionDistribution, 0, len(names))
	for _, name := range names {
		cc, err := qcl.CollectionCluster(ctx, name)
		if err != nil {
			continue
		}
		obs.Collections[name] = cc
		counts := map[uint64]int32{}
		for _, s := range cc.Shards {
			counts[s.PeerID]++
		}
		d := qdrantv1alpha1.CollectionDistribution{Collection: name, TransfersInFlight: int32(len(cc.Transfers))}
		// peer 순서는 ClusterInfo 정렬 순서(결정론) — 0 shard peer 도 표기해 불균형이 드러나게.
		for _, p := range ci.Peers {
			d.PerPeer = append(d.PerPeer, qdrantv1alpha1.PeerShards{Peer: strconv.FormatUint(p.ID, 10), Shards: counts[p.ID]})
		}
		dist = append(dist, d)
	}
	qc.Status.ShardDistribution = dist
	return obs
}

// SetupWithManager sets up the controller with the Manager.
func (r *QdrantClusterReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.QdrantClientFor == nil {
		// 프로덕션 기본: 클러스터 client Service DNS (오퍼레이터가 클러스터 안에서 동작 전제).
		r.QdrantClientFor = func(cluster *qdrantv1alpha1.QdrantCluster) qdrant.Client {
			return qdrant.NewHTTPClient(fmt.Sprintf("http://%s.%s.svc:%d",
				resources.ClientName(cluster), cluster.Namespace, resources.RESTPort))
		}
	}
	// SA1019 억제 사유: 신규 GetEventRecorder 는 events.EventRecorder(k8s.io/client-go/tools/events)
	// 를 반환하는데, 이는 구 record.EventRecorder 와 비등가다 — 평문 Event(obj,type,reason,msg) 가
	// 없고 Eventf(regarding,related,type,reason,action,note,…) 만 있어 related 객체·action 인자가 새로
	// 강제된다. Recorder.Event 로 경고를 내는 두 가드(Task 9 ImmutableFieldChanged / Task 10
	// ScaleDownRefused)의 의미를 바꾸지 않으려면 구 API 가 맞다(구 events API 는 아직 지원 — "미래
	// 릴리스 제거" 예고일 뿐). controller-runtime 자체도 동일 지점을 //nolint:staticcheck 로 억제하므로
	// (manager/internal.go·leaderelection) 마이그레이션 대신 표적 억제한다.
	r.Recorder = mgr.GetEventRecorderFor("qdrantcluster") //nolint:staticcheck // SA1019: 구 events API 유지 — 신규 GetEventRecorder 비등가(Event 미제공, action 필수)
	return ctrl.NewControllerManagedBy(mgr).
		// generation 변경(spec)만 트리거 — 자기 status 커밋으로 인한 재기록 루프 차단(§5.3).
		// Owns(STS 등) 이벤트와 RequeueAfter 는 영향받지 않는다.
		For(&qdrantv1alpha1.QdrantCluster{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Owns(&appsv1.StatefulSet{}).
		Owns(&corev1.Service{}).
		Owns(&corev1.ConfigMap{}).
		Owns(&corev1.ServiceAccount{}).
		Complete(r)
}
