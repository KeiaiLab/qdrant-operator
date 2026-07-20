package resources

import (
	"testing"

	qdrantv1alpha1 "github.com/keiailab/qdrant-operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestBuildServices(t *testing.T) {
	qc := &qdrantv1alpha1.QdrantCluster{ObjectMeta: metav1.ObjectMeta{Name: "c1", Namespace: "data"}}
	qc.Spec.ServiceType = corev1.ServiceTypeClusterIP
	h := BuildHeadlessService(qc)
	if h.Name != "c1-headless" || h.Spec.ClusterIP != corev1.ClusterIPNone {
		t.Fatalf("headless 불일치: %s cip=%s", h.Name, h.Spec.ClusterIP)
	}
	if !hasPort(h, 6335) {
		t.Fatalf("headless p2p 포트 누락")
	}
	c := BuildClientService(qc)
	if c.Name != "c1" || !hasPort(c, 6333) || !hasPort(c, 6334) {
		t.Fatalf("client 포트 불일치")
	}
}

func hasPort(s *corev1.Service, p int32) bool {
	for _, sp := range s.Spec.Ports {
		if sp.Port == p {
			return true
		}
	}
	return false
}
