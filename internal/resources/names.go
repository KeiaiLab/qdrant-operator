package resources

import qdrantv1alpha1 "github.com/keiailab/qdrant-operator/api/v1alpha1"

// 포트 상수 — Qdrant 컨테이너/서비스/빌더가 참조하는 SSOT (6333 REST / 6334 gRPC / 6335 Raft p2p).
// 소비처(ConfigMap·Service·StatefulSet 빌더)가 전부 이 패키지라 여기에 둔다 — CRD 타입은 포트를 노출하지 않음.
const (
	RESTPort = 6333
	GRPCPort = 6334
	P2PPort  = 6335
)

// 설정 파일명·마운트 경로 — ConfigMap 데이터 키(configmap.go)와 STS subPath 마운트(statefulset.go)가
// 공유하는 SSOT. 드리프트 시 마운트가 조용히 깨지므로 상수화한다.
const (
	InitScriptFile = "initialize.sh"
	ProdConfigFile = "production.yaml"
	ConfigMountDir = "/qdrant/config"
)

// STS 볼륨명 — VolumeMounts[].Name·Volumes[].Name·VCT ObjectMeta.Name 이 반드시 일치해야 하는 SSOT.
// 드리프트 시 파드가 "volume not found" 로 스케줄링 실패한다. qdrant-snapshots/qdrant-init 는
// readOnlyRootFilesystem=true 하에서 qdrant 가 쓰기하는 emptyDir(스냅샷 저장 / 초기화 마커) 볼륨이다.
const (
	ConfigVolumeName    = "qdrant-config"
	StorageVolumeName   = "qdrant-storage"
	SnapshotsVolumeName = "qdrant-snapshots"
	InitVolumeName      = "qdrant-init"
)

// 컨테이너 마운트 경로 — ConfigMountDir 와 동일 패턴. golden 컨테이너 volumeMounts SSOT.
// InitMountDir 는 마운트와 QDRANT_INIT_FILE_PATH env 값이 함께 참조하므로 상수화가 필수다.
const (
	StorageMountDir   = "/qdrant/storage"
	SnapshotsMountDir = "/qdrant/snapshots"
	InitMountDir      = "/qdrant/init"
)

// RunAsGroup 은 helm 차트 고정값(2000) — QdrantClusterSpec 에 대응 CR 필드가 없어(RunAsUser/FSGroup
// 과 달리 CR 에서 유도 불가) 여기 상수로 둔다. 컨테이너 securityContext.runAsGroup 에만 쓰인다.
const RunAsGroup int64 = 2000

func Name(qc *qdrantv1alpha1.QdrantCluster) string          { return qc.Name }
func HeadlessName(qc *qdrantv1alpha1.QdrantCluster) string  { return qc.Name + "-headless" }
func ClientName(qc *qdrantv1alpha1.QdrantCluster) string    { return qc.Name }
func ConfigMapName(qc *qdrantv1alpha1.QdrantCluster) string { return qc.Name }
func SAName(qc *qdrantv1alpha1.QdrantCluster) string        { return qc.Name }

func Labels(qc *qdrantv1alpha1.QdrantCluster) map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":       "qdrant",
		"app.kubernetes.io/instance":   qc.Name,
		"app.kubernetes.io/managed-by": "qdrant-operator",
	}
}
