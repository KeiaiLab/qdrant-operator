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

// STS 내 config 볼륨명 — VolumeMounts[].Name 과 Volumes[].Name 이 반드시 일치해야 하는 SSOT.
// 드리프트 시 파드가 "volume not found" 로 스케줄링 실패한다.
const ConfigVolumeName = "qdrant-config"

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
