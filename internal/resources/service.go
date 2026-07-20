package resources

import (
	qdrantv1alpha1 "github.com/keiailab/qdrant-operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

// port 는 name/port/targetPort 가 동일한 ServicePort 를 만든다. Protocol=TCP 는 golden 이
// 3개 포트 모두 명시 렌더링하므로(helm template 은 서버 디폴팅 없음) 여기서 명시한다.
func port(name string, p int32) corev1.ServicePort {
	return corev1.ServicePort{Name: name, Port: p, Protocol: corev1.ProtocolTCP, TargetPort: intstr.FromInt32(p)}
}

func BuildHeadlessService(qc *qdrantv1alpha1.QdrantCluster) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: HeadlessName(qc), Namespace: qc.Namespace, Labels: Labels(qc)},
		Spec: corev1.ServiceSpec{
			ClusterIP:                corev1.ClusterIPNone,
			PublishNotReadyAddresses: true,
			Selector:                 SelectorLabels(qc), // helm selector 와 동일 — 채택 시 엔드포인트 무변동
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
			Selector: SelectorLabels(qc), // helm selector 와 동일 — 채택 시 엔드포인트 무변동
			// golden client Service(비-headless)도 p2p(6335)를 노출한다 — 실측 `helm get manifest` 정합.
			Ports: []corev1.ServicePort{port("http", RESTPort), port("grpc", GRPCPort), port("p2p", P2PPort)},
		},
	}
}
