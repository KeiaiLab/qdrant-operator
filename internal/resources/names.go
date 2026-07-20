package resources

import qdrantv1alpha1 "github.com/keiailab/qdrant-operator/api/v1alpha1"

const (
	RESTPort = 6333
	GRPCPort = 6334
	P2PPort  = 6335
)

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
