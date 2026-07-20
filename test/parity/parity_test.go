/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package parity 는 오퍼레이터 빌더(internal/resources) 산출물이 실측 helm golden 과 기능적으로
// 등가임을 결정론으로 고정한다(Task 12). golden 은 testdata/helm-golden.yaml(이미 커밋된 helm
// template 실측 출력) — Fix 단계(commit 707d8fe)가 빌더를 이 golden 에 맞춰 확장했으므로, 본
// 테스트는 그 확장이 실제로 golden 과 일치하는지 재파싱 비교로 회귀를 잡는다.
package parity

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"

	qdrantv1alpha1 "github.com/keiailab/qdrant-operator/api/v1alpha1"
	resources "github.com/keiailab/qdrant-operator/internal/resources"
)

// goldenPath 는 helm template 실측 출력을 고정한 fixture(5 리소스: SA/CM/Service×2/STS).
const goldenPath = "testdata/helm-golden.yaml"

// docSeparator 는 helm 다중 문서 YAML 구분자 — 줄 전체가 '---' 인 라인.
var docSeparator = regexp.MustCompile(`(?m)^---\s*$`)

// goldenSet 은 golden YAML 5 문서를 타입 객체로 디코드한 결과.
type goldenSet struct {
	serviceAccount  *corev1.ServiceAccount
	configMap       *corev1.ConfigMap
	headlessService *corev1.Service
	clientService   *corev1.Service
	statefulSet     *appsv1.StatefulSet
}

// kindPeek 은 문서를 올바른 타입/슬롯으로 라우팅하기 위한 최소 디코드(kind + metadata.name 만).
type kindPeek struct {
	Kind     string `json:"kind"`
	Metadata struct {
		Name string `json:"name"`
	} `json:"metadata"`
}

// loadGolden 은 golden YAML(다중 문서, '---' 분리)을 sigs.k8s.io/yaml 로 타입 객체 5종으로 디코드한다.
func loadGolden(t *testing.T) goldenSet {
	t.Helper()
	raw, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("golden 파일 읽기 실패: %v", err)
	}

	var out goldenSet
	for _, doc := range splitYAMLDocuments(raw) {
		var peek kindPeek
		if err := yaml.Unmarshal(doc, &peek); err != nil {
			t.Fatalf("golden kind peek 디코드 실패: %v\n%s", err, doc)
		}
		switch peek.Kind {
		case "ServiceAccount":
			out.serviceAccount = &corev1.ServiceAccount{}
			decodeInto(t, doc, out.serviceAccount)
		case "ConfigMap":
			out.configMap = &corev1.ConfigMap{}
			decodeInto(t, doc, out.configMap)
		case "Service":
			svc := &corev1.Service{}
			decodeInto(t, doc, svc)
			if strings.HasSuffix(peek.Metadata.Name, "-headless") {
				out.headlessService = svc
			} else {
				out.clientService = svc
			}
		case "StatefulSet":
			out.statefulSet = &appsv1.StatefulSet{}
			decodeInto(t, doc, out.statefulSet)
		default:
			t.Fatalf("golden 에 알 수 없는 kind 등장: %s", peek.Kind)
		}
	}

	if out.serviceAccount == nil || out.configMap == nil || out.headlessService == nil ||
		out.clientService == nil || out.statefulSet == nil {
		t.Fatalf("golden 문서 누락(5종 중 일부 미검출): %+v", out)
	}
	return out
}

// splitYAMLDocuments 는 raw 를 '---' 단독 라인 기준으로 나누고 빈 조각(선두 구분자 앞 등)을 버린다.
func splitYAMLDocuments(raw []byte) [][]byte {
	parts := docSeparator.Split(string(raw), -1)
	docs := make([][]byte, 0, len(parts))
	for _, p := range parts {
		if trimmed := strings.TrimSpace(p); trimmed != "" {
			docs = append(docs, []byte(trimmed))
		}
	}
	return docs
}

func decodeInto(t *testing.T, doc []byte, target any) {
	t.Helper()
	if err := yaml.Unmarshal(doc, target); err != nil {
		t.Fatalf("golden 문서 디코드 실패: %v\n%s", err, doc)
	}
}

// buildTestCluster 는 golden 과 등가인 QdrantCluster CR 스펙을 구성한다 — 값은 전부 golden
// 리터럴에서 읽어 세팅한다(이름 platform-data-qdrant, image v1.18.2, resources 2/4Gi·250m/512Mi,
// storage 10Gi ceph-rbd, runAsUser 1000, fsGroup 3000, cluster.enabled=true/tls=false).
// +kubebuilder:default 마커는 apiserver 어드미션 시점에만 발동해 순수 Go 값 생성 경로(본 테스트)에는
// 미적용이므로 zero-value 에 의존하지 않고 전부 명시한다.
//
// Namespace 는 비워둔다 — golden 5 리소스 모두 metadata.namespace 키가 없어(디코드 시 "") legit-differ
// 제외 대상이 아닌 StatefulSet.metadata.namespace 를 그대로(값 일치로) 비교할 수 있게 한다.
func buildTestCluster() *qdrantv1alpha1.QdrantCluster {
	storageSize := resource.MustParse("10Gi")
	return &qdrantv1alpha1.QdrantCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "platform-data-qdrant"},
		Spec: qdrantv1alpha1.QdrantClusterSpec{
			Image:    qdrantv1alpha1.ImageSpec{Repository: "docker.io/qdrant/qdrant", Tag: "v1.18.2"},
			Replicas: 1,
			Resources: corev1.ResourceRequirements{
				Limits: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("2"),
					corev1.ResourceMemory: resource.MustParse("4Gi"),
				},
				Requests: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("250m"),
					corev1.ResourceMemory: resource.MustParse("512Mi"),
				},
			},
			Persistence: qdrantv1alpha1.PersistenceSpec{
				Size:             &storageSize,
				StorageClassName: "ceph-rbd",
				AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			},
			Config:      qdrantv1alpha1.ConfigSpec{ClusterEnabled: true, TLSEnabled: false},
			ServiceType: corev1.ServiceTypeClusterIP,
			RunAsUser:   1000,
			FSGroup:     3000,
		},
	}
}

// TestParity 는 5 리소스 빌더 산출물을 golden 과 필드별로 비교한다. legit-differ(비교 제외 대상)는
// 아래로 고정 — 어디에서도 assert 하지 않는다:
//   - ServiceAccount/ConfigMap/Service(headless)/Service(client) 의 metadata.labels, metadata.namespace
//   - StatefulSet.spec.volumeClaimTemplates[0].metadata.labels
//   - 오브젝트 metadata.labels/annotations 의 Helm·Flux 메타데이터(helm.sh/chart 등) 및 파드 템플릿
//     annotation checksum/config 의 "값"(오퍼레이터는 자체 sha256 을 넣으므로 helm 해시와 다름)
//
// 반면 spec.selector(STS/Service)와 파드 템플릿 labels 는 **비교 대상** — helm 배포 STS 를 제자리
// 채택(adoption)하려면 불변 selector 가 helm 과 정확히 일치해야 하기 때문(SelectorLabels).
func TestParity(t *testing.T) {
	golden := loadGolden(t)
	qc := buildTestCluster()

	t.Run("ServiceAccount", func(t *testing.T) {
		compareServiceAccount(t, resources.BuildServiceAccount(qc), golden.serviceAccount)
	})
	t.Run("ConfigMap", func(t *testing.T) {
		compareConfigMap(t, resources.BuildConfigMap(qc), golden.configMap)
	})
	t.Run("Service_headless", func(t *testing.T) {
		compareServiceCore(t, "Service(headless)", resources.BuildHeadlessService(qc), golden.headlessService)
	})
	t.Run("Service_client", func(t *testing.T) {
		compareServiceCore(t, "Service(client)", resources.BuildClientService(qc), golden.clientService)
	})
	t.Run("StatefulSet", func(t *testing.T) {
		compareStatefulSet(t, resources.BuildStatefulSet(qc), golden.statefulSet)
	})
}

// diffFields 는 필드별 불일치를 개별 t.Errorf 로 보고한다(요건: 전체 struct DeepEqual 통짜 금지 —
// 필드별 assert). 한 subtest 안에서 여러 필드가 어긋나도 전부 한 번에 드러나도록 Fatalf 대신 Errorf.
//
// 비교는 reflect.DeepEqual 대신 cmp.Diff 를 쓴다 — 이유 둘:
//  1. 사람이 읽을 수 있는 필드 단위 diff(포인터도 역참조해 실값을 보여줌)를 그대로 실패 메시지로 쓸 수
//     있다("어떻게 다른지" 요건).
//  2. corev1.ResourceList 값인 resource.Quantity 는 파싱 경로에 따라 내부 캐시 표현이 달라질 수 있어
//     reflect.DeepEqual 이 같은 값도 다르다고 오탐할 위험이 있다 — cmp 는 Quantity 가 노출하는
//     Equal(Quantity) bool 메서드를 자동으로 사용해(unexported 필드 접근 없이) 값 의미로만 비교한다.
type diffFields struct{ t *testing.T }

func (d diffFields) equal(field string, got, want any) {
	d.t.Helper()
	if diff := cmp.Diff(want, got); diff != "" {
		d.t.Errorf("%s 불일치 (-want +got):\n%s", field, diff)
	}
}

// compareServiceAccount 는 name 만 비교한다 — labels/namespace 는 legit-differ 제외 대상이고
// ServiceAccount 는 그 외 기능 필드를 갖지 않는다.
func compareServiceAccount(t *testing.T, got, want *corev1.ServiceAccount) {
	t.Helper()
	diffFields{t}.equal("ServiceAccount.metadata.name", got.Name, want.Name)
}

// compareConfigMap 은 name + initialize.sh/production.yaml 내용을 비교한다. labels/namespace 는
// legit-differ 제외 대상.
func compareConfigMap(t *testing.T, got, want *corev1.ConfigMap) {
	t.Helper()
	d := diffFields{t}
	d.equal("ConfigMap.metadata.name", got.Name, want.Name)
	for _, key := range []string{resources.InitScriptFile, resources.ProdConfigFile} {
		d.equal("ConfigMap.data["+key+"]", got.Data[key], want.Data[key])
	}
}

// compareServiceCore 는 headless/client Service 공통 기능 필드(name/type/clusterIP/
// publishNotReadyAddresses/selector/ports)를 비교한다. metadata.labels 만 legit-differ 제외.
// spec.selector 는 라이브 helm Service 채택 시 엔드포인트 무변동을 보장해야 하므로 golden 과
// 정확히 일치해야 한다.
func compareServiceCore(t *testing.T, label string, got, want *corev1.Service) {
	t.Helper()
	d := diffFields{t}
	d.equal(label+".metadata.name", got.Name, want.Name)
	d.equal(label+".spec.type", got.Spec.Type, want.Spec.Type)
	d.equal(label+".spec.clusterIP", got.Spec.ClusterIP, want.Spec.ClusterIP)
	d.equal(label+".spec.publishNotReadyAddresses", got.Spec.PublishNotReadyAddresses, want.Spec.PublishNotReadyAddresses)
	d.equal(label+".spec.selector", got.Spec.Selector, want.Spec.Selector)
	d.equal(label+".spec.ports", got.Spec.Ports, want.Spec.Ports)
}

// compareStatefulSet 은 STS 의 기능 필드를 비교한다:
//   - metadata.name/namespace
//   - spec.replicas(golden=1 — 빌더가 CR.Spec.Replicas 를 그대로 반영하는지. 이 비교가 없으면
//     replicas 변조가 parity 를 통과해버린다)
//   - spec.podManagementPolicy/serviceName/updateStrategy
//   - spec.selector + 파드 템플릿 labels(golden 과 정확 일치 — helm STS 제자리 채택의 전제.
//     selector 는 STS 불변 필드라 여기서 어긋나면 adoption 이 불가능하다)
//   - pod(compareStatefulSetPod) + 컨테이너(compareStatefulSetContainer, 컨테이너 name 포함) + VCT
//
// selector ⊆ 파드 라벨 상호 정합은 TestServiceSelectorTargetsPods 불변식이 별도로 지킨다.
func compareStatefulSet(t *testing.T, got, want *appsv1.StatefulSet) {
	t.Helper()
	d := diffFields{t}
	d.equal("StatefulSet.metadata.name", got.Name, want.Name)
	d.equal("StatefulSet.metadata.namespace", got.Namespace, want.Namespace)
	d.equal("StatefulSet.spec.replicas", got.Spec.Replicas, want.Spec.Replicas)
	d.equal("StatefulSet.spec.podManagementPolicy", got.Spec.PodManagementPolicy, want.Spec.PodManagementPolicy)
	d.equal("StatefulSet.spec.serviceName", got.Spec.ServiceName, want.Spec.ServiceName)
	d.equal("StatefulSet.spec.selector", got.Spec.Selector, want.Spec.Selector)
	d.equal("StatefulSet.spec.template.labels", got.Spec.Template.Labels, want.Spec.Template.Labels)
	d.equal("StatefulSet.spec.updateStrategy", got.Spec.UpdateStrategy, want.Spec.UpdateStrategy)

	compareStatefulSetPod(t, got.Spec.Template.Spec, want.Spec.Template.Spec)
	compareStatefulSetContainer(t, got.Spec.Template.Spec.Containers, want.Spec.Template.Spec.Containers)
	compareVolumeClaimTemplates(t, got.Spec.VolumeClaimTemplates, want.Spec.VolumeClaimTemplates)
}

// compareStatefulSetPod 는 파드 레벨 기능 필드(serviceAccountName/securityContext/volumes)를 비교한다.
// labels/annotations(checksum/config 등 Helm 메타데이터)는 legit-differ 제외 대상이라 건드리지 않는다.
func compareStatefulSetPod(t *testing.T, got, want corev1.PodSpec) {
	t.Helper()
	d := diffFields{t}
	d.equal("pod.spec.serviceAccountName", got.ServiceAccountName, want.ServiceAccountName)
	d.equal("pod.spec.securityContext", got.SecurityContext, want.SecurityContext)
	d.equal("pod.spec.volumes", got.Volumes, want.Volumes)
}

// compareStatefulSetContainer 는 단일 qdrant 컨테이너의 기능 필드 전부(name/image/command/args/env/
// lifecycle/imagePullPolicy/ports/readinessProbe/resources/securityContext/volumeMounts)를 비교한다.
// container.name(golden="qdrant")은 volumeMounts·probe 가 붙는 컨테이너 정체성이라 반드시 일치해야
// 하며, 이 비교가 없으면 name 변조가 parity 를 통과해버린다.
func compareStatefulSetContainer(t *testing.T, got, want []corev1.Container) {
	t.Helper()
	if len(got) != 1 || len(want) != 1 {
		t.Fatalf("container 개수 불일치: got=%d want=%d", len(got), len(want))
	}
	gotC, wantC := got[0], want[0]
	d := diffFields{t}
	d.equal("container.name", gotC.Name, wantC.Name)
	d.equal("container.image", gotC.Image, wantC.Image)
	d.equal("container.command", gotC.Command, wantC.Command)
	d.equal("container.args", gotC.Args, wantC.Args)
	d.equal("container.env", gotC.Env, wantC.Env)
	d.equal("container.imagePullPolicy", gotC.ImagePullPolicy, wantC.ImagePullPolicy)
	d.equal("container.ports", gotC.Ports, wantC.Ports)
	d.equal("container.readinessProbe", gotC.ReadinessProbe, wantC.ReadinessProbe)
	d.equal("container.lifecycle", gotC.Lifecycle, wantC.Lifecycle)
	d.equal("container.securityContext", gotC.SecurityContext, wantC.SecurityContext)
	d.equal("container.volumeMounts", gotC.VolumeMounts, wantC.VolumeMounts)
	d.equal("container.resources.limits", gotC.Resources.Limits, wantC.Resources.Limits)
	d.equal("container.resources.requests", gotC.Resources.Requests, wantC.Resources.Requests)
}

// compareVolumeClaimTemplates 는 VCT(size/storageClass/accessModes)를 비교한다. metadata.name 은
// 볼륨 마운트 바인딩(qdrant-storage)에 직결돼 비교하고, metadata.labels 는 legit-differ 제외 대상이라
// 건드리지 않는다.
func compareVolumeClaimTemplates(t *testing.T, got, want []corev1.PersistentVolumeClaim) {
	t.Helper()
	if len(got) != 1 || len(want) != 1 {
		t.Fatalf("volumeClaimTemplates 개수 불일치: got=%d want=%d", len(got), len(want))
	}
	gotVCT, wantVCT := got[0], want[0]
	d := diffFields{t}
	d.equal("vct.metadata.name", gotVCT.Name, wantVCT.Name)
	d.equal("vct.spec.accessModes", gotVCT.Spec.AccessModes, wantVCT.Spec.AccessModes)
	d.equal("vct.spec.storageClassName", gotVCT.Spec.StorageClassName, wantVCT.Spec.StorageClassName)
	d.equal("vct.spec.resources.requests", gotVCT.Spec.Resources.Requests, wantVCT.Spec.Resources.Requests)
}

// TestServiceSelectorTargetsPods 는 오퍼레이터가 만드는 Service.spec.selector 가 STS 파드 템플릿
// 라벨의 부분집합인지(= selector 가 실제로 그 파드를 잡는지) 검증하는 불변식이다. golden 값 비교가
// 아니라 빌더 산출물끼리의 상호 정합 검사 — Service selector 와 STS 파드 라벨은 helm/오퍼레이터 라벨
// 스킴 차이 때문에 golden 비교에서 legit-differ 로 빠져 있어(TestParity), 둘이 서로 어긋나 selector 가
// 0개 파드를 잡는 회귀(프로덕션 트래픽 0-엔드포인트 장애)를 어디서도 못 잡는 사각이 있었다. 본
// 불변식이 그 사각을 메운다.
func TestServiceSelectorTargetsPods(t *testing.T) {
	qc := buildTestCluster()
	podLabels := resources.BuildStatefulSet(qc).Spec.Template.Labels

	cases := []struct {
		label string
		svc   *corev1.Service
	}{
		{"Service(headless)", resources.BuildHeadlessService(qc)},
		{"Service(client)", resources.BuildClientService(qc)},
	}
	for _, c := range cases {
		assertSelectorSelectsPods(t, c.label, c.svc.Spec.Selector, podLabels)
	}
}

// assertSelectorSelectsPods 는 selector 가 비어있지 않고(빈 selector 는 파드를 특정하지 못함) 모든
// (key,value) 가 podLabels 에 동일 값으로 존재하는지 확인한다 — 하나라도 어긋나면 그 Service 는 대상
// 파드를 0개 잡아 엔드포인트가 비게 된다.
func assertSelectorSelectsPods(t *testing.T, label string, selector, podLabels map[string]string) {
	t.Helper()
	if len(selector) == 0 {
		t.Errorf("%s.spec.selector 가 비어 있음 — 파드를 특정하지 못해 0-엔드포인트 위험", label)
		return
	}
	for k, v := range selector {
		pv, ok := podLabels[k]
		if !ok {
			t.Errorf("%s.spec.selector[%q]=%q 가 STS 파드 라벨에 없음 — selector 가 파드를 못 잡음(0-엔드포인트)", label, k, v)
			continue
		}
		if pv != v {
			t.Errorf("%s.spec.selector[%q]=%q 가 STS 파드 라벨 값 %q 와 불일치 — selector 가 파드를 못 잡음(0-엔드포인트)", label, k, v, pv)
		}
	}
}
