package resources

import (
	"crypto/sha256"
	"fmt"

	qdrantv1alpha1 "github.com/keiailab/qdrant-operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ConfigChecksum 은 ConfigMap 데이터(initialize.sh + production.yaml)의 sha256 — STS 파드 템플릿
// annotation(checksum/config)에 넣어 설정 변경 시 자동 롤링 재기동을 유발한다(helm 차트와 동일 의미).
// 값 자체는 helm 의 해시(렌더된 매니페스트 기준)와 다르므로 helm→오퍼레이터 채택 시 1회 롤링이 발생한다.
func ConfigChecksum(qc *qdrantv1alpha1.QdrantCluster) string {
	h := sha256.Sum256([]byte(initScript(qc) + productionYAML(qc)))
	return fmt.Sprintf("%x", h)
}

func initScript(qc *qdrantv1alpha1.QdrantCluster) string {
	seed := fmt.Sprintf("http://%s-0.%s:%d", Name(qc), HeadlessName(qc), P2PPort)
	return fmt.Sprintf(`#!/bin/sh
echo "Soft limits"
ulimit -a -S
echo "Hard limits"
ulimit -a -H
ulimit -n $(ulimit -Hn)
SET_INDEX=${HOSTNAME##*-}
echo "Starting initializing for pod $SET_INDEX"
if [ "$SET_INDEX" = "0" ]; then
  exec ./entrypoint.sh --uri '%s'
else
  exec ./entrypoint.sh --bootstrap '%s' --uri 'http://%s-'"$SET_INDEX"'.%s:%d'
fi
`, seed, seed, Name(qc), HeadlessName(qc), P2PPort)
}

// snapshotsYAML 은 S3 보관일 때만 storage 블록을 낸다. 그 외에는 **빈 문자열**이라
// 기존 클러스터의 렌더 산출물이 한 바이트도 바뀌지 않는다(helm golden parity 유지).
// 자격(access/secret)과 엔드포인트는 여기 넣지 않는다 — ConfigMap 은 평문이고,
// 그 셋은 statefulset 의 env(secretKeyRef) 경로로만 간다.
func snapshotsYAML(qc *qdrantv1alpha1.QdrantCluster) string {
	sn := qc.Spec.Snapshots
	if sn == nil || sn.Storage != qdrantv1alpha1.SnapshotStorageS3 || sn.S3 == nil {
		return ""
	}

	region := sn.S3.Region
	if region == "" {
		region = DefaultS3Region
	}

	return fmt.Sprintf(`storage:
  snapshots_config:
    snapshots_storage: s3
    s3_config:
      bucket: %s
      region: %s
`, sn.S3.Bucket, region)
}

func productionYAML(qc *qdrantv1alpha1.QdrantCluster) string {
	tls := "false"
	if qc.Spec.Config.TLSEnabled {
		tls = "true"
	}
	return snapshotsYAML(qc) + fmt.Sprintf(`cluster:
  consensus:
    tick_period_ms: 100
  enabled: %t
  p2p:
    enable_tls: %s
    port: %d
service:
  enable_tls: %s
`, qc.Spec.Config.ClusterEnabled, tls, P2PPort, tls)
}

func BuildConfigMap(qc *qdrantv1alpha1.QdrantCluster) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      ConfigMapName(qc),
			Namespace: qc.Namespace,
			Labels:    Labels(qc),
		},
		Data: map[string]string{
			InitScriptFile: initScript(qc),
			ProdConfigFile: productionYAML(qc),
		},
	}
}
