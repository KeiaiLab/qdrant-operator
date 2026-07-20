package resources

import (
	"fmt"

	qdrantv1alpha1 "github.com/keiailab/qdrant-operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

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

func productionYAML(qc *qdrantv1alpha1.QdrantCluster) string {
	tls := "false"
	if qc.Spec.Config.TLSEnabled {
		tls = "true"
	}
	return fmt.Sprintf(`cluster:
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
			"initialize.sh":   initScript(qc),
			"production.yaml": productionYAML(qc),
		},
	}
}
