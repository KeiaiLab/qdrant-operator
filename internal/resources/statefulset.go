package resources

import (
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
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
	fsGroupChangePolicy := corev1.FSGroupChangeAlways
	sc := qc.Spec.Persistence.StorageClassName
	image := qc.Spec.Image.Repository + ":" + qc.Spec.Image.Tag

	// resources 는 CRD default 마커가 없어(다른 spec 필드와 달리) 미지정 CR 이면 빈 값이다 —
	// golden(helm 차트 고정값)과 어긋나지 않도록 빌더에서 fallback default 를 채운다. 사용자가
	// limits/requests 를 하나라도 지정하면 그대로 통과(honor).
	res := qc.Spec.Resources
	if res.Limits == nil && res.Requests == nil {
		res = defaultResources()
	}

	probe := &corev1.Probe{
		ProbeHandler:        corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: "/readyz", Port: intstr.FromInt32(RESTPort)}},
		InitialDelaySeconds: 5, PeriodSeconds: 5, FailureThreshold: 6,
		SuccessThreshold: 1, TimeoutSeconds: 1,
	}

	return &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: Name(qc), Namespace: qc.Namespace, Labels: Labels(qc)},
		Spec: appsv1.StatefulSetSpec{
			Replicas:            &replicas,
			ServiceName:         HeadlessName(qc),
			PodManagementPolicy: appsv1.ParallelPodManagement,
			UpdateStrategy: appsv1.StatefulSetUpdateStrategy{
				Type:          appsv1.RollingUpdateStatefulSetStrategyType,
				RollingUpdate: &appsv1.RollingUpdateStatefulSetStrategy{Partition: ptrInt32(0)},
			},
			// selector 는 STS 불변 필드 — helm 차트 selector 와 동일한 SelectorLabels 를 써야
			// 기존 helm-배포 STS 를 삭제 없이 제자리 채택(adoption)할 수 있다.
			Selector: &metav1.LabelSelector{MatchLabels: SelectorLabels(qc)},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: SelectorLabels(qc),
					// 설정(sha256) 변경 → 템플릿 해시 변경 → 자동 롤링 재기동 (helm 과 동일 의미).
					Annotations: map[string]string{"checksum/config": ConfigChecksum(qc)},
				},
				Spec: corev1.PodSpec{
					ServiceAccountName: SAName(qc),
					// pod-level: fsGroup/fsGroupChangePolicy/seccompProfile — runAsUser 는 golden 상
					// container-level 전용이라 여기 두지 않는다.
					SecurityContext: &corev1.PodSecurityContext{
						FSGroup:             &fsGroup,
						FSGroupChangePolicy: &fsGroupChangePolicy,
						SeccompProfile:      &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
					},
					NodeSelector: qc.Spec.NodeSelector,
					Tolerations:  qc.Spec.Tolerations,
					Affinity:     qc.Spec.Affinity,
					Containers: []corev1.Container{{
						Name:            AppName,
						Image:           image,
						ImagePullPolicy: corev1.PullIfNotPresent,
						// golden: 인터프리터(/bin/bash -c)와 스크립트 경로를 command/args 로 분리.
						// args 는 qdrant 이미지 WORKDIR(/qdrant) 기준 상대경로.
						Command: []string{"/bin/bash", "-c"},
						Args:    []string{"./config/initialize.sh"},
						Env: []corev1.EnvVar{
							{Name: "QDRANT_INIT_FILE_PATH", Value: InitMountDir + "/.qdrant-initialized"},
						},
						Resources: res,
						Ports: []corev1.ContainerPort{
							{Name: "http", ContainerPort: RESTPort, Protocol: corev1.ProtocolTCP},
							{Name: "grpc", ContainerPort: GRPCPort, Protocol: corev1.ProtocolTCP},
							{Name: "p2p", ContainerPort: P2PPort, Protocol: corev1.ProtocolTCP},
						},
						ReadinessProbe: probe,
						// preStop sleep 3 — SIGTERM 전 3초 유예로 롤링업데이트/스케일다운 시 서비스
						// 엔드포인트에서 안전하게 이탈(무중단).
						Lifecycle: &corev1.Lifecycle{
							PreStop: &corev1.LifecycleHandler{
								Exec: &corev1.ExecAction{Command: []string{"sleep", "3"}},
							},
						},
						// 컨테이너 하드닝 — runAsGroup(2000)만 차트 고정 상수, 나머지는 golden 리터럴.
						SecurityContext: &corev1.SecurityContext{
							RunAsUser:                &runAsUser,
							RunAsGroup:               ptrInt64(RunAsGroup),
							RunAsNonRoot:             ptrBool(true),
							AllowPrivilegeEscalation: ptrBool(false),
							Privileged:               ptrBool(false),
							ReadOnlyRootFilesystem:   ptrBool(true),
							Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
							SeccompProfile:           &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
						},
						// readOnlyRootFilesystem=true 라 쓰기 경로는 emptyDir 마운트로 뺀다(snapshots/init).
						VolumeMounts: []corev1.VolumeMount{
							{Name: StorageVolumeName, MountPath: StorageMountDir},
							{Name: ConfigVolumeName, MountPath: ConfigMountDir + "/" + InitScriptFile, SubPath: InitScriptFile},
							{Name: ConfigVolumeName, MountPath: ConfigMountDir + "/" + ProdConfigFile, SubPath: ProdConfigFile},
							{Name: SnapshotsVolumeName, MountPath: SnapshotsMountDir},
							{Name: InitVolumeName, MountPath: InitMountDir},
						},
					}},
					Volumes: []corev1.Volume{
						{
							Name: ConfigVolumeName,
							VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{
								LocalObjectReference: corev1.LocalObjectReference{Name: ConfigMapName(qc)},
								DefaultMode:          ptrInt32(0o755),
							}},
						},
						{Name: SnapshotsVolumeName, VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
						{Name: InitVolumeName, VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
					},
				},
			},
			VolumeClaimTemplates: []corev1.PersistentVolumeClaim{{
				ObjectMeta: metav1.ObjectMeta{Name: StorageVolumeName},
				Spec: corev1.PersistentVolumeClaimSpec{
					AccessModes:      qc.Spec.Persistence.AccessModes,
					StorageClassName: &sc,
					Resources:        corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: *qc.Spec.Persistence.Size}}, // Size 는 포인터(default 발동) — apiserver 라운드트립 후 non-nil 보장
				},
			}},
		},
	}
}

// defaultResources 는 helm 차트 고정 resources(golden) — resources 미지정 CR 의 fallback.
func defaultResources() corev1.ResourceRequirements {
	return corev1.ResourceRequirements{
		Limits: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("2"),
			corev1.ResourceMemory: resource.MustParse("4Gi"),
		},
		Requests: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("250m"),
			corev1.ResourceMemory: resource.MustParse("512Mi"),
		},
	}
}

func ptrInt32(i int32) *int32 { return &i }
func ptrInt64(i int64) *int64 { return &i }
func ptrBool(b bool) *bool    { return &b }
