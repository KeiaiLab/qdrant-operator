package resources

import (
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	qdrantv1alpha1 "github.com/keiailab/qdrant-operator/api/v1alpha1"
)

// PDBName 은 PodDisruptionBudget 이름 — STS 와 동일 이름을 쓰면 다른 kind 라 충돌하지 않고
// 소유·추적이 단순하다.
func PDBName(qc *qdrantv1alpha1.QdrantCluster) string { return qc.Name }

// BuildPodDisruptionBudget 은 자발적 중단(노드 drain·업그레이드) 시 최소 1 파드를 보장한다.
//
// replicas < 2 에서는 nil 을 반환한다 — 단일 파드에 minAvailable=1 을 걸면 노드 drain 이
// 영구 차단되어(축출 불가) 운영을 막는다. HA 는 replicas>=2 + 컬렉션 RF>=2 가 함께여야
// 성립하므로, PDB 도 그 구간에서만 의미를 갖는다.
func BuildPodDisruptionBudget(qc *qdrantv1alpha1.QdrantCluster) *policyv1.PodDisruptionBudget {
	if qc.Spec.Replicas < 2 {
		return nil
	}
	minAvailable := intstr.FromInt32(1)
	return &policyv1.PodDisruptionBudget{
		ObjectMeta: metav1.ObjectMeta{Name: PDBName(qc), Namespace: qc.Namespace, Labels: Labels(qc)},
		Spec: policyv1.PodDisruptionBudgetSpec{
			MinAvailable: &minAvailable,
			Selector:     &metav1.LabelSelector{MatchLabels: SelectorLabels(qc)},
		},
	}
}
