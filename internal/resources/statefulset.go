package resources

import (
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	qdrantv1alpha1 "github.com/keiailab/qdrant-operator/api/v1alpha1"
)

// BuildStatefulSet 은 QdrantCluster 스펙으로부터 실 클러스터 StatefulSet 을 구성한다.
// podManagementPolicy=Parallel + headless serviceName 조합은 라이브 클러스터 실측값 그대로 —
// Task 12 golden(helm template) parity 비교 대상이라 필드 임의 추가/누락 금지.
func BuildStatefulSet(qc *qdrantv1alpha1.QdrantCluster) *appsv1.StatefulSet {
	replicas := qc.Spec.Replicas
	runAsUser, fsGroup := qc.Spec.RunAsUser, qc.Spec.FSGroup
	sc := qc.Spec.Persistence.StorageClassName
	image := qc.Spec.Image.Repository + ":" + qc.Spec.Image.Tag

	probe := &corev1.Probe{
		ProbeHandler:        corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: "/readyz", Port: intstr.FromInt32(RESTPort)}},
		InitialDelaySeconds: 5, PeriodSeconds: 5, FailureThreshold: 6,
	}

	return &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: Name(qc), Namespace: qc.Namespace, Labels: Labels(qc)},
		Spec: appsv1.StatefulSetSpec{
			Replicas:            &replicas,
			ServiceName:         HeadlessName(qc),
			PodManagementPolicy: appsv1.ParallelPodManagement,
			UpdateStrategy:      appsv1.StatefulSetUpdateStrategy{Type: appsv1.RollingUpdateStatefulSetStrategyType},
			Selector:            &metav1.LabelSelector{MatchLabels: Labels(qc)},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: Labels(qc)},
				Spec: corev1.PodSpec{
					ServiceAccountName: SAName(qc),
					SecurityContext:    &corev1.PodSecurityContext{RunAsUser: &runAsUser, FSGroup: &fsGroup},
					NodeSelector:       qc.Spec.NodeSelector,
					Tolerations:        qc.Spec.Tolerations,
					Affinity:           qc.Spec.Affinity,
					Containers: []corev1.Container{{
						Name:      "qdrant",
						Image:     image,
						Command:   []string{"/bin/sh", "-c", ConfigMountDir + "/" + InitScriptFile},
						Resources: qc.Spec.Resources,
						Ports: []corev1.ContainerPort{
							{Name: "http", ContainerPort: RESTPort},
							{Name: "grpc", ContainerPort: GRPCPort},
							{Name: "p2p", ContainerPort: P2PPort},
						},
						ReadinessProbe: probe,
						VolumeMounts: []corev1.VolumeMount{
							{Name: "qdrant-storage", MountPath: "/qdrant/storage"},
							{Name: ConfigVolumeName, MountPath: ConfigMountDir + "/" + InitScriptFile, SubPath: InitScriptFile},
							{Name: ConfigVolumeName, MountPath: ConfigMountDir + "/" + ProdConfigFile, SubPath: ProdConfigFile},
						},
					}},
					Volumes: []corev1.Volume{{
						Name: ConfigVolumeName,
						VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{
							LocalObjectReference: corev1.LocalObjectReference{Name: ConfigMapName(qc)},
							DefaultMode:          ptrInt32(0o755),
						}},
					}},
				},
			},
			VolumeClaimTemplates: []corev1.PersistentVolumeClaim{{
				ObjectMeta: metav1.ObjectMeta{Name: "qdrant-storage"},
				Spec: corev1.PersistentVolumeClaimSpec{
					AccessModes:      qc.Spec.Persistence.AccessModes,
					StorageClassName: &sc,
					Resources:        corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: *qc.Spec.Persistence.Size}}, // Size 는 포인터(default 발동) — apiserver 라운드트립 후 non-nil 보장
				},
			}},
		},
	}
}

func ptrInt32(i int32) *int32 { return &i }
