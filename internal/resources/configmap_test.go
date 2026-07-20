package resources

import (
	"strings"
	"testing"

	qdrantv1alpha1 "github.com/keiailab/qdrant-operator/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestBuildConfigMap_InitScript(t *testing.T) {
	qc := &qdrantv1alpha1.QdrantCluster{ObjectMeta: metav1.ObjectMeta{Name: "platform-data-qdrant", Namespace: "data"}}
	qc.Spec.Config.ClusterEnabled = true
	cm := BuildConfigMap(qc)
	init := cm.Data["initialize.sh"]
	// pod-0 = seed(--uri), pod-N = --bootstrap to pod-0 (실측 로직)
	if !strings.Contains(init, "--uri 'http://platform-data-qdrant-0.platform-data-qdrant-headless:6335'") {
		t.Fatalf("pod-0 seed URI 불일치:\n%s", init)
	}
	if !strings.Contains(init, "--bootstrap 'http://platform-data-qdrant-0.platform-data-qdrant-headless:6335'") {
		t.Fatalf("pod-N bootstrap 불일치")
	}
	prod := cm.Data["production.yaml"]
	if !strings.Contains(prod, "enabled: true") || !strings.Contains(prod, "port: 6335") {
		t.Fatalf("production.yaml cluster 설정 불일치:\n%s", prod)
	}
}
