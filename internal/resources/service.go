package resources

import (
	qdrantv1alpha1 "github.com/keiailab/qdrant-operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

func port(name string, p int32) corev1.ServicePort {
	return corev1.ServicePort{Name: name, Port: p, TargetPort: intstr.FromInt32(p)}
}

func BuildHeadlessService(qc *qdrantv1alpha1.QdrantCluster) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: HeadlessName(qc), Namespace: qc.Namespace, Labels: Labels(qc)},
		Spec: corev1.ServiceSpec{
			ClusterIP:                corev1.ClusterIPNone,
			PublishNotReadyAddresses: true,
			Selector:                 Labels(qc),
			Ports:                    []corev1.ServicePort{port("http", RESTPort), port("grpc", GRPCPort), port("p2p", P2PPort)},
		},
	}
}

func BuildClientService(qc *qdrantv1alpha1.QdrantCluster) *corev1.Service {
	svcType := qc.Spec.ServiceType
	if svcType == "" {
		svcType = corev1.ServiceTypeClusterIP
	}
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: ClientName(qc), Namespace: qc.Namespace, Labels: Labels(qc), Annotations: qc.Spec.ServiceAnnotations},
		Spec: corev1.ServiceSpec{
			Type:     svcType,
			Selector: Labels(qc),
			Ports:    []corev1.ServicePort{port("http", RESTPort), port("grpc", GRPCPort)},
		},
	}
}
