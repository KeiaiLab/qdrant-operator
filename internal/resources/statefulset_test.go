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
	// updateStrategy
	if sts.Spec.UpdateStrategy.Type != appsv1.RollingUpdateStatefulSetStrategyType {
		t.Fatalf("updateStrategy=%s", sts.Spec.UpdateStrategy.Type)
	}
	// VCT 스토리지 크기(10Gi) — Size 포인터 default 회귀 가드
	if got := vct.Spec.Resources.Requests.Storage().String(); got != "10Gi" {
		t.Fatalf("VCT size=%s (want 10Gi)", got)
	}
	// VCT accessModes
	if len(vct.Spec.AccessModes) != 1 || vct.Spec.AccessModes[0] != corev1.ReadWriteOnce {
		t.Fatalf("accessModes=%v", vct.Spec.AccessModes)
	}
	// Command → initialize.sh 실행
	if len(c.Command) < 3 || c.Command[len(c.Command)-1] != "/qdrant/config/initialize.sh" {
		t.Fatalf("command=%v", c.Command)
	}
	// ConfigMap subPath 마운트 (initialize.sh + production.yaml)
	var mInit, mProd bool
	for _, vm := range c.VolumeMounts {
		if vm.SubPath == "initialize.sh" {
			mInit = true
		}
		if vm.SubPath == "production.yaml" {
			mProd = true
		}
	}
	if !mInit || !mProd {
		t.Fatalf("subPath 마운트 누락: initialize.sh=%v production.yaml=%v", mInit, mProd)
	}
}
