package resources

import (
	"testing"

	qdrantv1alpha1 "github.com/keiailab/qdrant-operator/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestBuildServiceAccount(t *testing.T) {
	qc := &qdrantv1alpha1.QdrantCluster{ObjectMeta: metav1.ObjectMeta{Name: "c1", Namespace: "data"}}
	sa := BuildServiceAccount(qc)
	if sa.Name != "c1" || sa.Namespace != "data" {
		t.Fatalf("이름/네임스페이스 불일치: %s/%s", sa.Name, sa.Namespace)
	}
	if sa.Labels["app.kubernetes.io/instance"] != "c1" {
		t.Fatalf("label 누락")
	}
}
