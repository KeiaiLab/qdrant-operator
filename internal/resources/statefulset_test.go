package resources

import (
	"strings"
	"testing"

	qdrantv1alpha1 "github.com/keiailab/qdrant-operator/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// statefulSetFixture 는 golden(helm-golden.yaml)과 등가인 최소 스펙으로 STS 를 만든다.
// resources 는 일부러 미지정 — 빌더 fallback default(golden 값)를 검증하기 위함.
func statefulSetFixture() *appsv1.StatefulSet {
	qc := &qdrantv1alpha1.QdrantCluster{ObjectMeta: metav1.ObjectMeta{Name: "c1", Namespace: "data"}}
	qc.Spec.Replicas = 1
	qc.Spec.Image = qdrantv1alpha1.ImageSpec{Repository: "qdrant/qdrant", Tag: "v1.18.2"}
	tenGi := resource.MustParse("10Gi")
	qc.Spec.Persistence = qdrantv1alpha1.PersistenceSpec{Size: &tenGi, StorageClassName: "ceph-rbd", AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}}
	qc.Spec.RunAsUser, qc.Spec.FSGroup = 1000, 3000
	return BuildStatefulSet(qc)
}

func wantBoolPtr(t *testing.T, name string, got *bool, want bool) {
	t.Helper()
	if got == nil {
		t.Fatalf("%s 누락(nil, want %v)", name, want)
	}
	if *got != want {
		t.Fatalf("%s=%v (want %v)", name, *got, want)
	}
}

func wantInt64Ptr(t *testing.T, name string, got *int64, want int64) {
	t.Helper()
	if got == nil {
		t.Fatalf("%s 누락(nil, want %d)", name, want)
	}
	if *got != want {
		t.Fatalf("%s=%d (want %d)", name, *got, want)
	}
}

// 스테이징 실측(2026-07-21)에서 발견된 unwired 필드 회귀 방지 — retentionPolicy=Delete 인데
// QdrantCluster 삭제 후 PVC 2개 잔존. envtest 는 PVC GC 가 없어 이 매핑 누락을 못 잡는다.
func TestBuildStatefulSet_RetentionPolicy매핑(t *testing.T) {
	// 미지정(=Retain default) → 필드 비설정 (helm-채택 STS 와 diff 0)
	if got := statefulSetFixture().Spec.PersistentVolumeClaimRetentionPolicy; got != nil {
		t.Fatalf("Retain/미지정은 비설정이어야 함: %+v", got)
	}

	qc := &qdrantv1alpha1.QdrantCluster{ObjectMeta: metav1.ObjectMeta{Name: "c1", Namespace: "data"}}
	qc.Spec.Replicas = 1
	qc.Spec.Image = qdrantv1alpha1.ImageSpec{Repository: "qdrant/qdrant", Tag: "v1.18.2"}
	oneGi := resource.MustParse("1Gi")
	qc.Spec.Persistence = qdrantv1alpha1.PersistenceSpec{Size: &oneGi, StorageClassName: "ceph-rbd", RetentionPolicy: qdrantv1alpha1.RetentionDelete}
	p := BuildStatefulSet(qc).Spec.PersistentVolumeClaimRetentionPolicy
	if p == nil {
		t.Fatal("Delete 인데 PersistentVolumeClaimRetentionPolicy 비설정 — unwired 재발")
	}
	if p.WhenDeleted != appsv1.DeletePersistentVolumeClaimRetentionPolicyType || p.WhenScaled != appsv1.DeletePersistentVolumeClaimRetentionPolicyType {
		t.Fatalf("whenDeleted/whenScaled 둘 다 Delete 여야 함: %+v", p)
	}
}

func TestBuildStatefulSet_Core(t *testing.T) {
	sts := statefulSetFixture()

	if *sts.Spec.Replicas != 1 {
		t.Fatal("replicas")
	}
	if sts.Spec.ServiceName != "c1-headless" {
		t.Fatal("serviceName")
	}
	if sts.Spec.PodManagementPolicy != appsv1.ParallelPodManagement {
		t.Fatal("podMgmt")
	}
	c := sts.Spec.Template.Spec.Containers[0]
	if c.Image != "qdrant/qdrant:v1.18.2" {
		t.Fatalf("image=%s", c.Image)
	}
	if c.ImagePullPolicy != corev1.PullIfNotPresent {
		t.Fatalf("imagePullPolicy=%s (want IfNotPresent)", c.ImagePullPolicy)
	}

	// updateStrategy — RollingUpdate + partition 0 (golden L248-251)
	if sts.Spec.UpdateStrategy.Type != appsv1.RollingUpdateStatefulSetStrategyType {
		t.Fatalf("updateStrategy=%s", sts.Spec.UpdateStrategy.Type)
	}
	if ru := sts.Spec.UpdateStrategy.RollingUpdate; ru == nil || ru.Partition == nil || *ru.Partition != 0 {
		t.Fatalf("updateStrategy.rollingUpdate.partition=%v (want 0)", sts.Spec.UpdateStrategy.RollingUpdate)
	}

	// VCT (name/size/storageClass/accessModes)
	vct := sts.Spec.VolumeClaimTemplates[0]
	if vct.Name != "qdrant-storage" {
		t.Fatalf("VCT name=%s (want qdrant-storage)", vct.Name)
	}
	if vct.Spec.StorageClassName == nil || *vct.Spec.StorageClassName != "ceph-rbd" {
		t.Fatal("storageClass")
	}
	if got := vct.Spec.Resources.Requests.Storage().String(); got != "10Gi" {
		t.Fatalf("VCT size=%s (want 10Gi)", got)
	}
	if len(vct.Spec.AccessModes) != 1 || vct.Spec.AccessModes[0] != corev1.ReadWriteOnce {
		t.Fatalf("accessModes=%v", vct.Spec.AccessModes)
	}
}

func TestBuildStatefulSet_Entrypoint(t *testing.T) {
	c := statefulSetFixture().Spec.Template.Spec.Containers[0]

	// command/args — golden: [/bin/bash,-c] + [./config/initialize.sh] (분리, golden L164-168)
	if len(c.Command) != 2 || c.Command[0] != "/bin/bash" || c.Command[1] != "-c" {
		t.Fatalf("command=%v (want [/bin/bash -c])", c.Command)
	}
	if len(c.Args) != 1 || c.Args[0] != "./config/initialize.sh" {
		t.Fatalf("args=%v (want [./config/initialize.sh])", c.Args)
	}

	// env QDRANT_INIT_FILE_PATH (golden L169-171)
	var envFound bool
	for _, e := range c.Env {
		if e.Name == "QDRANT_INIT_FILE_PATH" {
			envFound = true
			if e.Value != "/qdrant/init/.qdrant-initialized" {
				t.Fatalf("QDRANT_INIT_FILE_PATH=%s", e.Value)
			}
		}
	}
	if !envFound {
		t.Fatal("env QDRANT_INIT_FILE_PATH 누락")
	}

	// lifecycle preStop sleep 3 (golden L174-179)
	if c.Lifecycle == nil || c.Lifecycle.PreStop == nil || c.Lifecycle.PreStop.Exec == nil {
		t.Fatal("lifecycle preStop exec 누락")
	}
	if cmd := c.Lifecycle.PreStop.Exec.Command; len(cmd) != 2 || cmd[0] != "sleep" || cmd[1] != "3" {
		t.Fatalf("preStop=%v (want [sleep 3])", c.Lifecycle.PreStop.Exec.Command)
	}
}

func TestBuildStatefulSet_PortsProbeResources(t *testing.T) {
	c := statefulSetFixture().Spec.Template.Spec.Containers[0]

	// 포트 3종 + protocol TCP (golden L182-190)
	for _, want := range []struct {
		name string
		port int32
	}{{"http", 6333}, {"grpc", 6334}, {"p2p", 6335}} {
		found := false
		for _, p := range c.Ports {
			if p.Name == want.name && p.ContainerPort == want.port {
				found = true
				if p.Protocol != corev1.ProtocolTCP {
					t.Fatalf("포트 %s protocol=%q (want TCP)", want.name, p.Protocol)
				}
			}
		}
		if !found {
			t.Fatalf("포트 %s/%d 누락", want.name, want.port)
		}
	}

	// resources — 미지정 CR 이면 빌더가 golden default 를 채운다 (golden L200-206)
	if c.Resources.Limits.Cpu().String() != "2" || c.Resources.Limits.Memory().String() != "4Gi" {
		t.Fatalf("resources.limits=%v (want cpu=2 mem=4Gi)", c.Resources.Limits)
	}
	if c.Resources.Requests.Cpu().String() != "250m" || c.Resources.Requests.Memory().String() != "512Mi" {
		t.Fatalf("resources.requests=%v (want cpu=250m mem=512Mi)", c.Resources.Requests)
	}

	// readinessProbe — 전 필드 (golden L191-199)
	rp := c.ReadinessProbe
	if rp == nil || rp.HTTPGet == nil {
		t.Fatal("readinessProbe/httpGet 누락")
	}
	if rp.HTTPGet.Path != "/readyz" || rp.HTTPGet.Port.IntValue() != 6333 {
		t.Fatalf("readinessProbe httpGet=%v", rp.HTTPGet)
	}
	if rp.InitialDelaySeconds != 5 || rp.PeriodSeconds != 5 || rp.FailureThreshold != 6 || rp.SuccessThreshold != 1 || rp.TimeoutSeconds != 1 {
		t.Fatalf("readinessProbe 불일치: init=%d period=%d fail=%d success=%d timeout=%d", rp.InitialDelaySeconds, rp.PeriodSeconds, rp.FailureThreshold, rp.SuccessThreshold, rp.TimeoutSeconds)
	}
}

func TestBuildStatefulSet_SecurityContext(t *testing.T) {
	sts := statefulSetFixture()

	// 컨테이너 securityContext — 하드닝 8필드 (golden L207-218)
	csc := sts.Spec.Template.Spec.Containers[0].SecurityContext
	if csc == nil {
		t.Fatal("container securityContext 누락")
	}
	wantInt64Ptr(t, "container runAsUser", csc.RunAsUser, 1000)
	wantInt64Ptr(t, "container runAsGroup", csc.RunAsGroup, 2000)
	wantBoolPtr(t, "runAsNonRoot", csc.RunAsNonRoot, true)
	wantBoolPtr(t, "allowPrivilegeEscalation", csc.AllowPrivilegeEscalation, false)
	wantBoolPtr(t, "privileged", csc.Privileged, false)
	wantBoolPtr(t, "readOnlyRootFilesystem", csc.ReadOnlyRootFilesystem, true)
	if csc.Capabilities == nil || len(csc.Capabilities.Drop) != 1 || csc.Capabilities.Drop[0] != "ALL" {
		t.Fatalf("capabilities.drop=%v (want [ALL])", csc.Capabilities)
	}
	if csc.SeccompProfile == nil || csc.SeccompProfile.Type != corev1.SeccompProfileTypeRuntimeDefault {
		t.Fatal("container seccompProfile != RuntimeDefault")
	}

	// pod securityContext — fsGroup/fsGroupChangePolicy/seccompProfile, runAsUser 는 없어야 함 (golden L233-237)
	psc := sts.Spec.Template.Spec.SecurityContext
	if psc == nil {
		t.Fatal("pod securityContext 누락")
	}
	wantInt64Ptr(t, "pod fsGroup", psc.FSGroup, 3000)
	if psc.FSGroupChangePolicy == nil || *psc.FSGroupChangePolicy != corev1.FSGroupChangeAlways {
		t.Fatalf("pod fsGroupChangePolicy=%v (want Always)", psc.FSGroupChangePolicy)
	}
	if psc.SeccompProfile == nil || psc.SeccompProfile.Type != corev1.SeccompProfileTypeRuntimeDefault {
		t.Fatal("pod seccompProfile != RuntimeDefault")
	}
	if psc.RunAsUser != nil {
		t.Fatalf("pod runAsUser=%v — golden 은 container-level 전용, pod-level 은 없어야 함", *psc.RunAsUser)
	}
}

func TestBuildStatefulSet_Volumes(t *testing.T) {
	sts := statefulSetFixture()
	c := sts.Spec.Template.Spec.Containers[0]

	// volumeMounts — storage + config subPath(initialize.sh/production.yaml) + snapshots/init (golden L219-231)
	var mStorage, mInit, mProd, mSnap, mInitVol bool
	for _, vm := range c.VolumeMounts {
		switch {
		case vm.Name == "qdrant-storage" && vm.MountPath == "/qdrant/storage":
			mStorage = true
		case vm.SubPath == "initialize.sh":
			mInit = true
		case vm.SubPath == "production.yaml":
			mProd = true
		case vm.Name == "qdrant-snapshots" && vm.MountPath == "/qdrant/snapshots":
			mSnap = true
		case vm.Name == "qdrant-init" && vm.MountPath == "/qdrant/init":
			mInitVol = true
		}
	}
	if !mStorage || !mInit || !mProd || !mSnap || !mInitVol {
		t.Fatalf("volumeMounts 누락: storage=%v initialize.sh=%v production.yaml=%v snapshots=%v init=%v", mStorage, mInit, mProd, mSnap, mInitVol)
	}

	// volumes — config(configMap) + snapshots/init(emptyDir) (golden L239-247)
	var vConfig, vSnap, vInit bool
	for _, v := range sts.Spec.Template.Spec.Volumes {
		switch v.Name {
		case "qdrant-config":
			vConfig = v.ConfigMap != nil
		case "qdrant-snapshots":
			vSnap = v.EmptyDir != nil
		case "qdrant-init":
			vInit = v.EmptyDir != nil
		}
	}
	if !vConfig || !vSnap || !vInit {
		t.Fatalf("volumes 누락/타입불일치: config(configMap)=%v snapshots(emptyDir)=%v init(emptyDir)=%v", vConfig, vSnap, vInit)
	}
}

// HA 기본값(v0.5.0) — replicas>=2 에서 soft anti-affinity 자동, 지정 시 존중, 1이면 없음.
func TestBuildStatefulSet_HA기본_antiAffinity(t *testing.T) {
	mk := func(replicas int32, aff *corev1.Affinity) *appsv1.StatefulSet {
		qc := &qdrantv1alpha1.QdrantCluster{ObjectMeta: metav1.ObjectMeta{Name: "ha", Namespace: "data"}}
		qc.Spec.Replicas = replicas
		qc.Spec.Image = qdrantv1alpha1.ImageSpec{Repository: "qdrant/qdrant", Tag: "v1.18.3"}
		g := resource.MustParse("1Gi")
		qc.Spec.Persistence = qdrantv1alpha1.PersistenceSpec{Size: &g, StorageClassName: "ceph-rbd"}
		qc.Spec.Affinity = aff
		return BuildStatefulSet(qc)
	}
	if a := mk(1, nil).Spec.Template.Spec.Affinity; a != nil {
		t.Fatalf("replicas=1 은 anti-affinity 미주입이어야 함: %+v", a)
	}
	a := mk(2, nil).Spec.Template.Spec.Affinity
	if a == nil || a.PodAntiAffinity == nil || len(a.PodAntiAffinity.PreferredDuringSchedulingIgnoredDuringExecution) != 1 {
		t.Fatalf("replicas=2 는 soft anti-affinity 자동 주입이어야 함: %+v", a)
	}
	term := a.PodAntiAffinity.PreferredDuringSchedulingIgnoredDuringExecution[0]
	if term.PodAffinityTerm.TopologyKey != "kubernetes.io/hostname" {
		t.Fatalf("topologyKey: %s", term.PodAffinityTerm.TopologyKey)
	}
	if a.PodAntiAffinity.RequiredDuringSchedulingIgnoredDuringExecution != nil {
		t.Fatal("required 는 노드 부족 시 스케줄 차단 — preferred 여야 함")
	}
	// 사용자 지정은 그대로 존중(덮어쓰기 금지).
	user := &corev1.Affinity{NodeAffinity: &corev1.NodeAffinity{}}
	if got := mk(2, user).Spec.Template.Spec.Affinity; got != user {
		t.Fatal("사용자 affinity 를 덮어씀")
	}
}

func TestBuildPodDisruptionBudget(t *testing.T) {
	mk := func(replicas int32) *policyv1.PodDisruptionBudget {
		qc := &qdrantv1alpha1.QdrantCluster{ObjectMeta: metav1.ObjectMeta{Name: "ha", Namespace: "data"}}
		qc.Spec.Replicas = replicas
		return BuildPodDisruptionBudget(qc)
	}
	if p := mk(1); p != nil {
		t.Fatal("replicas=1 에 PDB 를 만들면 노드 drain 이 영구 차단된다")
	}
	p := mk(2)
	if p == nil || p.Spec.MinAvailable == nil || p.Spec.MinAvailable.IntValue() != 1 {
		t.Fatalf("replicas>=2 는 minAvailable=1 PDB: %+v", p)
	}
	if p.Spec.Selector.MatchLabels["app.kubernetes.io/instance"] != "ha" {
		t.Fatalf("selector: %+v", p.Spec.Selector)
	}
}

// apiKeyQC 는 apiKey 배선 검증용 최소 스펙 — golden fixture 와 분리해 env 추가가
// parity 를 깨지 않는지(미설정 케이스)도 독립적으로 본다.
func apiKeyQC() *qdrantv1alpha1.QdrantCluster {
	qc := &qdrantv1alpha1.QdrantCluster{ObjectMeta: metav1.ObjectMeta{Name: "c1", Namespace: "data"}}
	qc.Spec.Replicas = 1
	qc.Spec.Image = qdrantv1alpha1.ImageSpec{Repository: "qdrant/qdrant", Tag: "v1.18.2"}
	// Size 는 포인터 필드 — CRD default 는 apiserver 라운드트립에서만 발동하므로 빌더 단위
	// 테스트에선 명시하지 않으면 nil deref panic. golden fixture 와 동일하게 채운다.
	tenGi := resource.MustParse("10Gi")
	qc.Spec.Persistence = qdrantv1alpha1.PersistenceSpec{Size: &tenGi, StorageClassName: "ceph-rbd", AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}}
	qc.Spec.RunAsUser, qc.Spec.FSGroup = 1000, 3000
	return qc
}

func findEnv(c corev1.Container, name string) *corev1.EnvVar {
	for i := range c.Env {
		if c.Env[i].Name == name {
			return &c.Env[i]
		}
	}
	return nil
}

func TestBuildStatefulSet_APIKey_WriteKey(t *testing.T) {
	qc := apiKeyQC()
	qc.Spec.APIKey = &qdrantv1alpha1.SecretKeyRef{Name: "qdrant-auth", Key: "api-key"}

	c := BuildStatefulSet(qc).Spec.Template.Spec.Containers[0]
	e := findEnv(c, "QDRANT__SERVICE__API_KEY")
	if e == nil {
		t.Fatal("apiKey 설정 시 env QDRANT__SERVICE__API_KEY 가 주입돼야 함")
	}
	// Secret 값은 valueFrom.secretKeyRef 로만 — Value(평문) 금지.
	if e.Value != "" {
		t.Fatalf("Value 는 비어야 함(평문 노출 금지), got %q", e.Value)
	}
	if e.ValueFrom == nil || e.ValueFrom.SecretKeyRef == nil {
		t.Fatal("QDRANT__SERVICE__API_KEY 는 secretKeyRef 여야 함")
	}
	if ref := e.ValueFrom.SecretKeyRef; ref.Name != "qdrant-auth" || ref.Key != "api-key" {
		t.Fatalf("secretKeyRef=%+v (want name=qdrant-auth key=api-key)", ref)
	}
}

func TestBuildStatefulSet_APIKey_ReadOnlyKey(t *testing.T) {
	qc := apiKeyQC()
	qc.Spec.ReadOnlyAPIKey = &qdrantv1alpha1.SecretKeyRef{Name: "qdrant-auth", Key: "read-only-key"}

	c := BuildStatefulSet(qc).Spec.Template.Spec.Containers[0]
	e := findEnv(c, "QDRANT__SERVICE__READ_ONLY_API_KEY")
	if e == nil {
		t.Fatal("readOnlyApiKey 설정 시 env QDRANT__SERVICE__READ_ONLY_API_KEY 가 주입돼야 함")
	}
	if e.ValueFrom == nil || e.ValueFrom.SecretKeyRef == nil {
		t.Fatal("QDRANT__SERVICE__READ_ONLY_API_KEY 는 secretKeyRef 여야 함")
	}
	if ref := e.ValueFrom.SecretKeyRef; ref.Name != "qdrant-auth" || ref.Key != "read-only-key" {
		t.Fatalf("secretKeyRef=%+v (want name=qdrant-auth key=read-only-key)", ref)
	}
	// 쓰기 키를 안 줬으면 쓰기 API_KEY 는 주입되지 않아야 한다(두 키 독립).
	if findEnv(c, "QDRANT__SERVICE__API_KEY") != nil {
		t.Fatal("readOnly 만 설정했는데 쓰기 QDRANT__SERVICE__API_KEY 가 주입됨")
	}
}

func TestBuildStatefulSet_APIKey_KeyDefault(t *testing.T) {
	qc := apiKeyQC()
	qc.Spec.APIKey = &qdrantv1alpha1.SecretKeyRef{Name: "qdrant-auth"} // Key 미지정

	c := BuildStatefulSet(qc).Spec.Template.Spec.Containers[0]
	e := findEnv(c, "QDRANT__SERVICE__API_KEY")
	if e == nil || e.ValueFrom == nil || e.ValueFrom.SecretKeyRef == nil {
		t.Fatal("env QDRANT__SERVICE__API_KEY 누락")
	}
	// Key 미지정이면 빈 Secret 키(런타임 실패) 대신 'api-key' 로 방어.
	if got := e.ValueFrom.SecretKeyRef.Key; got != "api-key" {
		t.Fatalf("Key 미지정 시 기본 'api-key' 여야 함, got %q", got)
	}
}

func TestBuildStatefulSet_APIKey_미설정parity(t *testing.T) {
	// 인증 키를 안 준 CR 은 인증 env 를 하나도 추가하지 않아야 한다 — golden(helm) parity.
	c := BuildStatefulSet(apiKeyQC()).Spec.Template.Spec.Containers[0]
	if findEnv(c, "QDRANT__SERVICE__API_KEY") != nil || findEnv(c, "QDRANT__SERVICE__READ_ONLY_API_KEY") != nil {
		t.Fatal("apiKey 미설정 CR 에 인증 env 가 주입됨 — golden parity 위반")
	}
	// env 는 QDRANT_INIT_FILE_PATH 하나뿐이어야 한다.
	if len(c.Env) != 1 {
		t.Fatalf("미설정 CR env 개수=%d (want 1: QDRANT_INIT_FILE_PATH 만)", len(c.Env))
	}
}

// snapshotFixture 는 statefulSetFixture 와 같은 최소 스펙에 스냅샷 설정만 얹은 CR 이다.
func snapshotFixture() *qdrantv1alpha1.QdrantCluster {
	qc := &qdrantv1alpha1.QdrantCluster{ObjectMeta: metav1.ObjectMeta{Name: "c1", Namespace: "data"}}
	qc.Spec.Replicas = 1
	qc.Spec.Image = qdrantv1alpha1.ImageSpec{Repository: "qdrant/qdrant", Tag: "v1.18.2"}
	tenGi := resource.MustParse("10Gi")
	qc.Spec.Persistence = qdrantv1alpha1.PersistenceSpec{Size: &tenGi, StorageClassName: "ceph-rbd", AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}}
	qc.Spec.RunAsUser, qc.Spec.FSGroup = 1000, 3000
	return qc
}

func TestStatefulSet_스냅샷S3Env(t *testing.T) {
	qc := snapshotFixture()
	qc.Spec.Snapshots = &qdrantv1alpha1.SnapshotsSpec{
		Storage: qdrantv1alpha1.SnapshotStorageS3,
		S3: &qdrantv1alpha1.S3StorageSpec{
			Bucket: "b", EndpointURL: "http://rgw.svc:80",
			Credentials: qdrantv1alpha1.S3CredentialsRef{Name: "rgw-creds"},
		},
	}
	env := BuildStatefulSet(qc).Spec.Template.Spec.Containers[0].Env

	byName := map[string]corev1.EnvVar{}
	for _, e := range env {
		byName[e.Name] = e
	}

	ep, ok := byName["QDRANT__STORAGE__SNAPSHOTS_CONFIG__S3_CONFIG__ENDPOINT_URL"]
	if !ok || ep.Value != "http://rgw.svc:80" {
		t.Fatalf("엔드포인트 env: %+v", ep)
	}

	// 자격은 반드시 secretKeyRef 로만 — Value 가 채워져 있으면 평문 유출이다.
	for name, key := range map[string]string{
		"QDRANT__STORAGE__SNAPSHOTS_CONFIG__S3_CONFIG__ACCESS_KEY": "AWS_ACCESS_KEY_ID",
		"QDRANT__STORAGE__SNAPSHOTS_CONFIG__S3_CONFIG__SECRET_KEY": "AWS_SECRET_ACCESS_KEY",
	} {
		e, ok := byName[name]
		if !ok {
			t.Fatalf("%s 누락", name)
		}
		if e.Value != "" || e.ValueFrom == nil || e.ValueFrom.SecretKeyRef == nil {
			t.Fatalf("%s 가 secretKeyRef 가 아님: %+v", name, e)
		}
		if e.ValueFrom.SecretKeyRef.Name != "rgw-creds" || e.ValueFrom.SecretKeyRef.Key != key {
			t.Fatalf("%s 참조 불일치: %+v", name, e.ValueFrom.SecretKeyRef)
		}
	}

	// 미지정이면 env 가 하나도 늘지 않는다(golden parity).
	qc.Spec.Snapshots = nil
	for _, e := range BuildStatefulSet(qc).Spec.Template.Spec.Containers[0].Env {
		if strings.Contains(e.Name, "SNAPSHOTS_CONFIG") {
			t.Fatalf("미지정인데 env 가 샜다: %s", e.Name)
		}
	}
}

func TestStatefulSet_TLS마운트와프로브(t *testing.T) {
	qc := snapshotFixture()
	qc.Spec.Config.TLSEnabled = true
	qc.Spec.Config.TLS = &qdrantv1alpha1.TLSSpec{SecretName: "qdrant-tls"}
	sts := BuildStatefulSet(qc)
	c := sts.Spec.Template.Spec.Containers[0]

	// 인증서는 Secret 에서 오고, 컨테이너 안 파일명은 qdrant 가 기대하는 이름으로 바뀐다.
	var vol *corev1.Volume
	for i := range sts.Spec.Template.Spec.Volumes {
		if sts.Spec.Template.Spec.Volumes[i].Name == TLSVolumeName {
			vol = &sts.Spec.Template.Spec.Volumes[i]
		}
	}
	if vol == nil || vol.Secret == nil {
		t.Fatalf("TLS 볼륨 누락: %+v", sts.Spec.Template.Spec.Volumes)
	}
	if vol.Secret.SecretName != "qdrant-tls" {
		t.Fatalf("Secret 이름: %s", vol.Secret.SecretName)
	}
	want := map[string]string{"tls.crt": TLSCertFile, "tls.key": TLSKeyFile, "ca.crt": TLSCACertFile}
	got := map[string]string{}
	for _, it := range vol.Secret.Items {
		got[it.Key] = it.Path
	}
	if len(got) != len(want) {
		t.Fatalf("키 매핑 수 불일치: %+v", got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("%s → %s (want %s)", k, got[k], v)
		}
	}

	var mounted bool
	for _, m := range c.VolumeMounts {
		if m.Name == TLSVolumeName && m.MountPath == TLSMountDir {
			mounted = true
		}
	}
	if !mounted {
		t.Fatalf("TLS 마운트 누락: %+v", c.VolumeMounts)
	}

	// service.enable_tls 가 켜지면 /readyz 는 HTTPS 다. 프로브가 HTTP 로 남으면
	// 파드가 영원히 Ready 가 되지 않는다.
	if c.ReadinessProbe.HTTPGet.Scheme != corev1.URISchemeHTTPS {
		t.Fatalf("프로브 scheme=%s (want HTTPS)", c.ReadinessProbe.HTTPGet.Scheme)
	}

	// 미설정이면 아무것도 늘지 않는다(golden parity).
	plain := statefulSetFixture()
	for _, v := range plain.Spec.Template.Spec.Volumes {
		if v.Name == TLSVolumeName {
			t.Fatal("TLS 미설정인데 볼륨이 생겼다")
		}
	}
	if plain.Spec.Template.Spec.Containers[0].ReadinessProbe.HTTPGet.Scheme == corev1.URISchemeHTTPS {
		t.Fatal("TLS 미설정인데 프로브가 HTTPS")
	}
}
