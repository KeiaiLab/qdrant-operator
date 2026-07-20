package resources

import (
	"testing"

	qdrantv1alpha1 "github.com/keiailab/qdrant-operator/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestBuildStatefulSet(t *testing.T) {
	qc := &qdrantv1alpha1.QdrantCluster{ObjectMeta: metav1.ObjectMeta{Name: "c1", Namespace: "data"}}
	qc.Spec.Replicas = 1
	qc.Spec.Image = qdrantv1alpha1.ImageSpec{Repository: "qdrant/qdrant", Tag: "v1.18.2"}
	tenGi := resource.MustParse("10Gi")
	qc.Spec.Persistence = qdrantv1alpha1.PersistenceSpec{Size: &tenGi, StorageClassName: "ceph-rbd", AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}}
	qc.Spec.RunAsUser, qc.Spec.FSGroup = 1000, 3000
	sts := BuildStatefulSet(qc)

	if *sts.Spec.Replicas != 1 {
		t.Fatal("replicas")
	}
	if sts.Spec.ServiceName != "c1-headless" {
		t.Fatal("serviceName")
	}
	if sts.Spec.PodManagementPolicy != appsv1.ParallelPodManagement {
		t.Fatal("podMgmt")
	}
	c := sts.Spec.Template.Spec.Containers[0]
	if c.Image != "qdrant/qdrant:v1.18.2" {
		t.Fatalf("image=%s", c.Image)
	}
	for _, want := range []int32{6333, 6334, 6335} {
		found := false
		for _, p := range c.Ports {
			if p.ContainerPort == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("포트 %d 누락", want)
		}
	}
	if *sts.Spec.Template.Spec.SecurityContext.RunAsUser != 1000 || *sts.Spec.Template.Spec.SecurityContext.FSGroup != 3000 {
		t.Fatal("securityContext")
	}
	vct := sts.Spec.VolumeClaimTemplates[0]
	if vct.Spec.StorageClassName == nil || *vct.Spec.StorageClassName != "ceph-rbd" {
		t.Fatal("storageClass")
	}
	if c.ReadinessProbe == nil {
		t.Fatal("readinessProbe 누락")
	}
}
