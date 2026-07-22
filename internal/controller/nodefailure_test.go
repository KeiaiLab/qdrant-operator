package controller

import (
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	qdrantv1alpha1 "github.com/keiailab/qdrant-operator/api/v1alpha1"
	resources "github.com/keiailab/qdrant-operator/internal/resources"
)

// 노드 장애 파드 정리(v0.6.0) — 발동 조건 4종을 순수 판정으로 검증한다.
// envtest 는 노드 condition 을 자유롭게 조작하기 번거로워 fake client 로 격리한다.
func nodeWith(name string, ready corev1.ConditionStatus, ago time.Duration) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{
			Type: corev1.NodeReady, Status: ready,
			LastTransitionTime: metav1.NewTime(time.Now().Add(-ago)),
		}}},
	}
}

func podOn(qc *qdrantv1alpha1.QdrantCluster, name, node string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: qc.Namespace, Labels: resources.SelectorLabels(qc)},
		Spec:       corev1.PodSpec{NodeName: node, Containers: []corev1.Container{{Name: "qdrant", Image: "x"}}},
	}
}

func newFixture(t *testing.T, replicas int32, objs ...runtime.Object) (*QdrantClusterReconciler, *qdrantv1alpha1.QdrantCluster) {
	t.Helper()
	s := runtime.NewScheme()
	if err := scheme.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	if err := qdrantv1alpha1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	qc := &qdrantv1alpha1.QdrantCluster{ObjectMeta: metav1.ObjectMeta{Name: "nf", Namespace: "data"}}
	qc.Spec.Replicas = replicas
	all := append([]runtime.Object{qc}, objs...)
	c := fake.NewClientBuilder().WithScheme(s).WithRuntimeObjects(all...).Build()
	return &QdrantClusterReconciler{Client: c, Scheme: s, Recorder: events.NewFakeRecorder(10)}, qc
}

func TestReconcileStuckPods(t *testing.T) {
	qcRef := &qdrantv1alpha1.QdrantCluster{ObjectMeta: metav1.ObjectMeta{Name: "nf", Namespace: "data"}}

	t.Run("죽은노드_grace초과_삭제", func(t *testing.T) {
		r, qc := newFixture(t, 2,
			nodeWith("dead", corev1.ConditionFalse, 10*time.Minute),
			podOn(qcRef, "nf-0", "dead"))
		if n := r.reconcileStuckPods(t.Context(), qc); n != 1 {
			t.Fatalf("grace 초과 죽은 노드의 파드를 정리해야 함: %d", n)
		}
	})

	t.Run("죽은노드_grace미달_보류", func(t *testing.T) {
		r, qc := newFixture(t, 2,
			nodeWith("dead", corev1.ConditionFalse, 2*time.Minute),
			podOn(qcRef, "nf-0", "dead"))
		if n := r.reconcileStuckPods(t.Context(), qc); n != 0 {
			t.Fatalf("grace(6m) 미달인데 삭제 — 일시 NotReady 를 장애로 오판: %d", n)
		}
	})

	t.Run("정상노드_무행동", func(t *testing.T) {
		r, qc := newFixture(t, 2,
			nodeWith("live", corev1.ConditionTrue, time.Hour),
			podOn(qcRef, "nf-0", "live"))
		if n := r.reconcileStuckPods(t.Context(), qc); n != 0 {
			t.Fatalf("Ready 노드의 파드를 삭제: %d", n)
		}
	})

	t.Run("단일replica_무행동", func(t *testing.T) {
		r, qc := newFixture(t, 1,
			nodeWith("dead", corev1.ConditionFalse, 30*time.Minute),
			podOn(qcRef, "nf-0", "dead"))
		if n := r.reconcileStuckPods(t.Context(), qc); n != 0 {
			t.Fatalf("replicas=1 은 대체 파드도 같은 상황 — 무행동이어야 함: %d", n)
		}
	})

	t.Run("노드객체부재_즉시정리", func(t *testing.T) {
		// 노드가 클러스터에서 제거된 경우(VM 삭제 등) — condition 을 볼 수 없어 즉시 장애로 본다.
		r, qc := newFixture(t, 2, podOn(qcRef, "nf-0", "vanished"))
		if n := r.reconcileStuckPods(t.Context(), qc); n != 1 {
			t.Fatalf("노드 객체 부재 시 정리해야 함: %d", n)
		}
	})

	t.Run("동시1개상한", func(t *testing.T) {
		r, qc := newFixture(t, 3,
			nodeWith("d1", corev1.ConditionFalse, 20*time.Minute),
			nodeWith("d2", corev1.ConditionFalse, 20*time.Minute),
			podOn(qcRef, "nf-0", "d1"), podOn(qcRef, "nf-1", "d2"))
		if n := r.reconcileStuckPods(t.Context(), qc); n != 1 {
			t.Fatalf("두 노드 동시 장애여도 1개만 정리해야 함(정족수 보호): %d", n)
		}
	})
}
