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

	// service.enable_tls 가 켜지면 /readyz 는 HTTPS 다. 프로브가 HTTP 로 남으면 파드가
	// 영원히 Ready 가 되지 않는다 — TLS 를 켰을 때만 나타나는 조용한 정지다.
	// 미설정이면 Scheme 은 빈 값(=HTTP)이라 golden 산출물이 바뀌지 않는다.
	var scheme corev1.URIScheme
	if qc.Spec.Config.TLSEnabled {
		scheme = corev1.URISchemeHTTPS
	}
	probe := &corev1.Probe{
		ProbeHandler:        corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: "/readyz", Port: intstr.FromInt32(RESTPort), Scheme: scheme}},
		InitialDelaySeconds: 5, PeriodSeconds: 5, FailureThreshold: 6,
		SuccessThreshold: 1, TimeoutSeconds: 1,
	}

	// retentionPolicy=Delete → STS 네이티브 PVC 자동정리(whenDeleted/whenScaled 모두 Delete —
	// scale-down 서수의 PVC 는 drain 이 데이터를 이미 대피시킨 뒤라 삭제해도 안전).
	// Retain(기본·미지정)은 k8s 기본 의미와 동일하므로 필드 비설정 — helm-채택 STS 와 diff 0 유지.
	var pvcRetention *appsv1.StatefulSetPersistentVolumeClaimRetentionPolicy
	if qc.Spec.Persistence.RetentionPolicy == qdrantv1alpha1.RetentionDelete {
		pvcRetention = &appsv1.StatefulSetPersistentVolumeClaimRetentionPolicy{
			WhenDeleted: appsv1.DeletePersistentVolumeClaimRetentionPolicyType,
			WhenScaled:  appsv1.DeletePersistentVolumeClaimRetentionPolicyType,
		}
	}

	return &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: Name(qc), Namespace: qc.Namespace, Labels: Labels(qc)},
		Spec: appsv1.StatefulSetSpec{
			Replicas:                             &replicas,
			ServiceName:                          HeadlessName(qc),
			PodManagementPolicy:                  appsv1.ParallelPodManagement,
			PersistentVolumeClaimRetentionPolicy: pvcRetention,
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
					Affinity:     resolveAffinity(qc),
					Containers: []corev1.Container{{
						Name:            AppName,
						Image:           image,
						ImagePullPolicy: corev1.PullIfNotPresent,
						// golden: 인터프리터(/bin/bash -c)와 스크립트 경로를 command/args 로 분리.
						// args 는 qdrant 이미지 WORKDIR(/qdrant) 기준 상대경로.
						Command: []string{"/bin/bash", "-c"},
						Args:    []string{"./config/initialize.sh"},
						Env: append([]corev1.EnvVar{
							{Name: "QDRANT_INIT_FILE_PATH", Value: InitMountDir + "/.qdrant-initialized"},
						}, append(apiKeyEnv(qc), snapshotS3Env(qc)...)...),
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
						VolumeMounts: append([]corev1.VolumeMount{
							{Name: StorageVolumeName, MountPath: StorageMountDir},
							{Name: ConfigVolumeName, MountPath: ConfigMountDir + "/" + InitScriptFile, SubPath: InitScriptFile},
							{Name: ConfigVolumeName, MountPath: ConfigMountDir + "/" + ProdConfigFile, SubPath: ProdConfigFile},
							{Name: SnapshotsVolumeName, MountPath: SnapshotsMountDir},
							{Name: InitVolumeName, MountPath: InitMountDir},
						}, tlsMount(qc)...),
					}},
					Volumes: append([]corev1.Volume{
						{
							Name: ConfigVolumeName,
							VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{
								LocalObjectReference: corev1.LocalObjectReference{Name: ConfigMapName(qc)},
								DefaultMode:          ptrInt32(0o755),
							}},
						},
						{Name: SnapshotsVolumeName, VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
						{Name: InitVolumeName, VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
					}, tlsVolume(qc)...),
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

// tlsVolume 은 인증서 Secret 을 볼륨으로 만든다. Secret 의 키 이름(cert-manager 기본값)을
// qdrant 설정이 기대하는 파일명으로 **바꿔서** 마운트한다 — 그래야 발급 도구의 관례와
// qdrant 의 관례를 둘 다 건드리지 않는다.
//
// 미설정이면 nil 이라 STS 산출물이 늘지 않는다(golden parity).
func tlsVolume(qc *qdrantv1alpha1.QdrantCluster) []corev1.Volume {
	tls := qc.Spec.Config.TLS
	if !qc.Spec.Config.TLSEnabled || tls == nil {
		return nil
	}

	certKey, keyKey, caKey := tlsSecretKeys(tls)
	return []corev1.Volume{{
		Name: TLSVolumeName,
		VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{
			SecretName: tls.SecretName,
			Items: []corev1.KeyToPath{
				{Key: certKey, Path: TLSCertFile},
				{Key: keyKey, Path: TLSKeyFile},
				{Key: caKey, Path: TLSCACertFile},
			},
		}},
	}}
}

func tlsMount(qc *qdrantv1alpha1.QdrantCluster) []corev1.VolumeMount {
	if !qc.Spec.Config.TLSEnabled || qc.Spec.Config.TLS == nil {
		return nil
	}
	return []corev1.VolumeMount{{Name: TLSVolumeName, MountPath: TLSMountDir, ReadOnly: true}}
}

// tlsSecretKeys 는 미지정 키를 CRD default 와 같은 값으로 방어한다.
func tlsSecretKeys(tls *qdrantv1alpha1.TLSSpec) (cert, key, ca string) {
	cert, key, ca = tls.CertKey, tls.KeyKey, tls.CACertKey
	if cert == "" {
		cert = DefaultTLSCertKey
	}
	if key == "" {
		key = DefaultTLSKeyKey
	}
	if ca == "" {
		ca = DefaultTLSCACertKey
	}
	return cert, key, ca
}

// apiKeyEnv 는 설정된 인증 키를 qdrant 이중언더스코어 env override 로 만든다. Secret 값은
// valueFrom.secretKeyRef 로만 주입해 ConfigMap(평문) 경로를 피한다. 미설정 CR 이면 nil 반환 →
// STS env 에 아무것도 추가되지 않아 golden(helm template) parity 를 유지한다.
func apiKeyEnv(qc *qdrantv1alpha1.QdrantCluster) []corev1.EnvVar {
	var env []corev1.EnvVar
	if ref := qc.Spec.APIKey; ref != nil {
		env = append(env, secretEnv("QDRANT__SERVICE__API_KEY", ref))
	}
	if ref := qc.Spec.ReadOnlyAPIKey; ref != nil {
		env = append(env, secretEnv("QDRANT__SERVICE__READ_ONLY_API_KEY", ref))
	}
	return env
}

// snapshotS3Env 는 S3 보관에 필요한 세 값을 qdrant 이중언더스코어 env 로 만든다.
// 엔드포인트는 비밀이 아니라 평문 value 로, 자격 둘은 secretKeyRef 로만 넣는다.
// 미설정이면 nil 이라 STS env 가 늘지 않는다(golden parity).
func snapshotS3Env(qc *qdrantv1alpha1.QdrantCluster) []corev1.EnvVar {
	sn := qc.Spec.Snapshots
	if sn == nil || sn.Storage != qdrantv1alpha1.SnapshotStorageS3 || sn.S3 == nil {
		return nil
	}

	const prefix = "QDRANT__STORAGE__SNAPSHOTS_CONFIG__S3_CONFIG__"
	creds := sn.S3.Credentials

	accessKey := creds.AccessKeyKey
	if accessKey == "" {
		accessKey = DefaultS3AccessKeyKey
	}
	secretKey := creds.SecretKeyKey
	if secretKey == "" {
		secretKey = DefaultS3SecretKeyKey
	}

	return []corev1.EnvVar{
		{Name: prefix + "ENDPOINT_URL", Value: sn.S3.EndpointURL},
		secretEnv(prefix+"ACCESS_KEY", &qdrantv1alpha1.SecretKeyRef{Name: creds.Name, Key: accessKey}),
		secretEnv(prefix+"SECRET_KEY", &qdrantv1alpha1.SecretKeyRef{Name: creds.Name, Key: secretKey}),
	}
}

// secretEnv 는 SecretKeyRef 를 secretKeyRef env 로 변환한다. Key 가 비면 'api-key' 로 방어
// (CRD default 와 동일값) — 빈 Secret 키 참조로 인한 런타임 실패를 막는다.
func secretEnv(name string, ref *qdrantv1alpha1.SecretKeyRef) corev1.EnvVar {
	key := ref.Key
	if key == "" {
		key = DefaultAPIKeySecretKey
	}
	return corev1.EnvVar{
		Name: name,
		ValueFrom: &corev1.EnvVarSource{
			SecretKeyRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: ref.Name},
				Key:                  key,
			},
		},
	}
}

// resolveAffinity 는 spec.affinity 를 그대로 존중하되, 미지정 + replicas>=2 이면 HA 기본값으로
// soft(preferred) pod anti-affinity 를 주입한다 — 같은 노드에 복제본이 몰려 노드 1대 장애가
// 전체 중단으로 번지는 것을 막는다. required 가 아닌 preferred 인 이유: 노드가 부족하면
// 스케줄 자체가 막혀 오히려 가용성을 해치기 때문이다(단일 노드 클러스터 호환).
func resolveAffinity(qc *qdrantv1alpha1.QdrantCluster) *corev1.Affinity {
	if qc.Spec.Affinity != nil || qc.Spec.Replicas < 2 {
		return qc.Spec.Affinity
	}
	return &corev1.Affinity{
		PodAntiAffinity: &corev1.PodAntiAffinity{
			PreferredDuringSchedulingIgnoredDuringExecution: []corev1.WeightedPodAffinityTerm{{
				Weight: 100,
				PodAffinityTerm: corev1.PodAffinityTerm{
					LabelSelector: &metav1.LabelSelector{MatchLabels: SelectorLabels(qc)},
					TopologyKey:   "kubernetes.io/hostname",
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
