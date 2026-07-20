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
	// headless: http/grpc/p2p 3종 + protocol TCP (golden L76-88)
	for _, p := range []int32{6333, 6334, 6335} {
		if !hasPort(h, p) {
			t.Fatalf("headless 포트 %d 누락", p)
		}
	}
	assertAllTCP(t, "headless", h)

	// client(비-headless): golden 정합 — http/grpc/p2p 3종 + protocol TCP (golden L111-123)
	c := BuildClientService(qc)
	if c.Name != "c1" {
		t.Fatalf("client name=%s", c.Name)
	}
	for _, p := range []int32{6333, 6334, 6335} {
		if !hasPort(c, p) {
			t.Fatalf("client 포트 %d 누락", p)
		}
	}
	assertAllTCP(t, "client", c)
}

func hasPort(s *corev1.Service, p int32) bool {
	for _, sp := range s.Spec.Ports {
		if sp.Port == p {
			return true
		}
	}
	return false
}

func assertAllTCP(t *testing.T, label string, s *corev1.Service) {
	t.Helper()
	for _, sp := range s.Spec.Ports {
		if sp.Protocol != corev1.ProtocolTCP {
			t.Fatalf("%s 포트 %s protocol=%q (want TCP)", label, sp.Name, sp.Protocol)
		}
	}
}
