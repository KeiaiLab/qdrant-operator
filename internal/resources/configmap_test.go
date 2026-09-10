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

func TestBuildConfigMap_스냅샷저장소(t *testing.T) {
	qc := &qdrantv1alpha1.QdrantCluster{ObjectMeta: metav1.ObjectMeta{Name: "q", Namespace: "data"}}

	// 미지정 — 렌더 산출물이 바뀌지 않아야 한다(기존 클러스터 회귀 0 · helm parity 유지).
	base := BuildConfigMap(qc).Data["production.yaml"]
	if strings.Contains(base, "snapshots_config") {
		t.Fatalf("스냅샷 미지정인데 설정이 새어 나옴:\n%s", base)
	}

	// Local — 그것이 qdrant 기본이므로 역시 아무것도 쓰지 않는다.
	qc.Spec.Snapshots = &qdrantv1alpha1.SnapshotsSpec{Storage: qdrantv1alpha1.SnapshotStorageLocal}
	if got := BuildConfigMap(qc).Data["production.yaml"]; got != base {
		t.Fatalf("Local 은 기본과 같아야 함:\n%s", got)
	}

	// S3 — 버킷·리전만 설정에 들어간다. 자격과 엔드포인트는 env 몫이다(평문 금지).
	qc.Spec.Snapshots = &qdrantv1alpha1.SnapshotsSpec{
		Storage: qdrantv1alpha1.SnapshotStorageS3,
		S3: &qdrantv1alpha1.S3StorageSpec{
			Bucket: "qdrant-backups", Region: "ap-northeast-2",
			EndpointURL: "http://rook-ceph-rgw-keiailab-rgw.rook-ceph.svc:80",
			Credentials: qdrantv1alpha1.S3CredentialsRef{Name: "rgw-creds"},
		},
	}
	prod := BuildConfigMap(qc).Data["production.yaml"]
	for _, want := range []string{"snapshots_storage: s3", "bucket: qdrant-backups", "region: ap-northeast-2"} {
		if !strings.Contains(prod, want) {
			t.Fatalf("%q 누락:\n%s", want, prod)
		}
	}
	if strings.Contains(prod, "rgw-creds") || strings.Contains(prod, "endpoint_url") {
		t.Fatalf("자격·엔드포인트가 ConfigMap 평문으로 샜다:\n%s", prod)
	}

	// Storage=S3 인데 s3 블록이 없으면 반쯤 설정된 qdrant 를 만들지 않는다.
	qc.Spec.Snapshots.S3 = nil
	if got := BuildConfigMap(qc).Data["production.yaml"]; got != base {
		t.Fatalf("s3 블록 없는 S3 는 무설정이어야 함:\n%s", got)
	}
}
