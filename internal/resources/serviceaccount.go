package resources

import (
	qdrantv1alpha1 "github.com/keiailab/qdrant-operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func BuildServiceAccount(qc *qdrantv1alpha1.QdrantCluster) *corev1.ServiceAccount {
	return &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      SAName(qc),
			Namespace: qc.Namespace,
			Labels:    Labels(qc),
		},
	}
}
