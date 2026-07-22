//go:build e2e
// +build e2e

/*
Copyright 2026 Keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

// Package e2e는 QdrantCluster 프로비저닝 스모크(Phase A, Task 14)를 담는다 — 실측 클러스터
// 위에서 오퍼레이터를 배포하고 실 CR 라이프사이클(적용→Ready→scale-up→삭제→데이터 잔존)을
// 검증한다. envtest(internal/controller)·parity(test/parity)와 달리 fake client 없이
// 실 kubectl/helm 프로세스만 사용한다.
//
// 실행 전제(플레이스홀더 아님 — 실제로 갖춰야 하는 것들):
//   - kind 클러스터(`make setup-test-e2e`) 또는 임의 클러스터의 scratch 네임스페이스. 후자를
//     쓸 때는 KUBECONFIG가 그 클러스터를 가리키고 있어야 하며, storageClassName을 그 클러스터에
//     실재하는 값으로 맞춰야 한다(E2E_STORAGE_CLASS 환경변수, 미설정 시 kind 기본값 "standard").
//   - docker 데몬 — BeforeSuite(e2e_suite_test.go)가 `make docker-build`로 매니저 이미지를 빌드한다.
//   - helm v3 CLI — 오퍼레이터 배포가 `deploy/chart`(Task 13)에 대한 `helm install`이다. 그
//     차트는 별도 Task 13 산출물이라 본 worktree에는 아직 없을 수 있다; 값 스키마는 최소
//     `image`(문자열, 매니저 이미지 참조) 하나를 노출한다고 가정한다(Task 13 스펙).
//   - `//go:build e2e` 태그로 `make test`(envtest 유닛/통합)에서는 제외된다. 실행은
//     `make test-e2e` 전용(Makefile이 -tags=e2e로 본 패키지만 골라 돌린다).
//
// 설치 안내(`helm install ...` 전체 커맨드)와 QdrantCluster 샘플은 README.md 참고.
//
// 정직한 한계(Phase A): step 5는 "새 peer가 Raft 합의에 join했는가"만 검증한다 — join한 빈
// peer로 기존 shard가 자동 재배치(reshard)되는지는 검증하지 않는다(자동 reshard는 Phase B).
package e2e

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	resources "github.com/keiailab/qdrant-operator/internal/resources"
	"github.com/keiailab/qdrant-operator/test/utils"
)

const (
	// smokeOperatorNamespace는 오퍼레이터를 배포하는 네임스페이스 — config/default 쿠스터마이즈
	// 기본값(namespace: qdrant-operator-system)과 동일하게 맞춘다. deploy/chart(Task 13)가 그
	// 쿠스터마이즈 렌더 출력(RBAC 등)을 그대로 export한 것이라 ClusterRoleBinding의 subject
	// 네임스페이스가 이 값으로 굳어 있을 가능성이 높다 — helm install -n도 반드시 이 값을 써야
	// RBAC subject와 실제 설치 네임스페이스가 어긋나지 않는다.
	smokeOperatorNamespace = "qdrant-operator-system"
	// smokeWorkloadNamespace는 QdrantCluster CR과 그 child(STS/PVC 등)를 담는 scratch
	// 네임스페이스 — 오퍼레이터 자신의 네임스페이스와 분리해 cluster-scoped RBAC(ClusterRole+
	// ClusterRoleBinding, config/rbac/role_binding.yaml)로 임의 네임스페이스를 리콘사일할 수
	// 있음을 함께 실증한다.
	smokeWorkloadNamespace = "qdrant-e2e-smoke"
	// smokeReleaseName은 오퍼레이터 helm 릴리스 이름.
	smokeReleaseName = "qdrant-operator-e2e"
	// smokeClusterName은 QdrantCluster CR 이름 — StatefulSet/Pod/PVC 이름이 전부 이 값에서
	// 결정론으로 파생된다(resources.Name == qc.Name, Pod = "<name>-<ordinal>",
	// PVC = "<StorageVolumeName>-<name>-<ordinal>").
	smokeClusterName = "qdrant-e2e"
)

// smokeStorageClassName은 QdrantCluster.spec.persistence.storageClassName에 쓸 StorageClass
// 이름을 고른다. kind 클러스터는 local-path-provisioner가 "standard"를 기본(default) SC로
// 제공하므로 그 값을 기본으로 삼고, scratch ns로 실클러스터를 겨냥할 때는
// E2E_STORAGE_CLASS(예: "ceph-rbd")로 덮어쓴다. CRD 자체 기본값("ceph-rbd", 운영 클러스터
// 전용)을 그대로 두면 kind에서는 PVC가 영원히 Pending이라 스모크가 통과할 수 없다.
func smokeStorageClassName() string {
	if v := os.Getenv("E2E_STORAGE_CLASS"); v != "" {
		return v
	}
	return "standard"
}

var _ = Describe("QdrantCluster 프로비저닝 스모크(Phase A)", Ordered, func() {
	BeforeAll(func() {
		By("kubectl create ns " + smokeWorkloadNamespace)
		cmd := exec.Command("kubectl", "create", "ns", smokeWorkloadNamespace)
		output, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "워크로드 네임스페이스 생성 실패:\n%s", output)
	})

	AfterAll(func() {
		By("워크로드 네임스페이스 삭제(QdrantCluster/STS/PVC 전체 정리)")
		cmd := exec.Command("kubectl", "delete", "ns", smokeWorkloadNamespace, "--ignore-not-found", "--timeout=2m")
		_, _ = utils.Run(cmd)

		By("helm uninstall " + smokeReleaseName)
		cmd = exec.Command("helm", "uninstall", smokeReleaseName, "-n", smokeOperatorNamespace)
		_, _ = utils.Run(cmd)

		By("오퍼레이터 네임스페이스 삭제")
		cmd = exec.Command("kubectl", "delete", "ns", smokeOperatorNamespace, "--ignore-not-found", "--timeout=2m")
		_, _ = utils.Run(cmd)
	})

	It("1. helm install로 deploy/chart에서 오퍼레이터를 배포한다", func() {
		By(fmt.Sprintf("helm install %s deploy/chart -n %s --create-namespace --set image=%s --wait --timeout=2m",
			smokeReleaseName, smokeOperatorNamespace, managerImage))
		cmd := exec.Command("helm", "install", smokeReleaseName, "deploy/chart",
			"-n", smokeOperatorNamespace,
			"--create-namespace",
			"--set", "image="+managerImage,
			"--wait", "--timeout=2m",
		)
		output, err := utils.Run(cmd)
		// helm install --wait는 Deployment의 availableReplicas가 스펙을 채울 때까지 블록하고,
		// 타임아웃 시 non-zero exit로 실패한다 — 이 한 번의 에러 체크가 "오퍼레이터가 실제로
		// 뜬 상태"까지 보증한다(단순 apply와 다름).
		Expect(err).NotTo(HaveOccurred(), "helm install 실패:\n%s", output)
	})

	It("2. QdrantCluster CR을 워크로드 네임스페이스에 적용한다", func() {
		crYAML := fmt.Sprintf(`apiVersion: qdrant.keiailab.com/v1alpha1
kind: QdrantCluster
metadata:
  name: %s
  namespace: %s
spec:
  replicas: 1
  config:
    clusterEnabled: true
  persistence:
    size: 1Gi
    storageClassName: %s
    retentionPolicy: Retain
`, smokeClusterName, smokeWorkloadNamespace, smokeStorageClassName())

		By("kubectl apply -f - (QdrantCluster, replicas=1)")
		cmd := exec.Command("kubectl", "apply", "-f", "-")
		cmd.Stdin = strings.NewReader(crYAML)
		output, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "QdrantCluster apply 실패:\n%s", output)
	})

	It("3. status.phase=Running을 대기한다", func() {
		By(fmt.Sprintf("kubectl get qdrantcluster %s -n %s -o jsonpath={.status.phase} == Running",
			smokeClusterName, smokeWorkloadNamespace))
		Eventually(waitClusterPhase(smokeClusterName, "Running"), 5*time.Minute, 5*time.Second).Should(Succeed())
	})

	It("4. kubectl exec + bash /dev/tcp로 GET /collections 200을 확인한다", func() {
		pod := smokeClusterName + "-0"
		By(fmt.Sprintf("kubectl exec %s -n %s -- bash -c 'exec 3<>/dev/tcp/127.0.0.1/%d; GET /collections' == 200",
			pod, smokeWorkloadNamespace, resources.RESTPort))
		Eventually(func(g Gomega) {
			status, body, err := execHTTPGet(smokeWorkloadNamespace, pod, "/collections")
			g.Expect(err).NotTo(HaveOccurred(), "exec HTTP GET 실패:\n%s", body)
			g.Expect(status).To(Equal(200), "GET /collections 상태코드 불일치: got=%d\n%s", status, body)
		}, 2*time.Minute, 5*time.Second).Should(Succeed())
	})

	It("5. replicas 1→2 scale-up 후 새 peer의 Raft join을 확인한다(/cluster peer 수=2)", func() {
		By(fmt.Sprintf(`kubectl patch qdrantcluster %s -n %s --type=merge -p '{"spec":{"replicas":2}}'`,
			smokeClusterName, smokeWorkloadNamespace))
		cmd := exec.Command("kubectl", "patch", "qdrantcluster", smokeClusterName,
			"-n", smokeWorkloadNamespace, "--type=merge", "-p", `{"spec":{"replicas":2}}`)
		output, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "scale-up patch 실패:\n%s", output)

		newPeerPod := smokeClusterName + "-1"
		By("kubectl wait pod " + newPeerPod + " --for=condition=Ready --timeout=3m")
		cmd = exec.Command("kubectl", "wait", "pod", newPeerPod,
			"-n", smokeWorkloadNamespace, "--for=condition=Ready", "--timeout=3m")
		output, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "새 peer 파드 Ready 대기 실패:\n%s", output)

		By(fmt.Sprintf("kubectl get qdrantcluster %s -n %s -o jsonpath={.status.phase} == Running(readyReplicas=2)",
			smokeClusterName, smokeWorkloadNamespace))
		Eventually(waitClusterPhase(smokeClusterName, "Running"), 3*time.Minute, 5*time.Second).Should(Succeed())

		By("GET /cluster 로 Raft peer 수=2 확인 (응답 바디의 \"uri\": 등장 횟수로 카운트)")
		Eventually(func(g Gomega) {
			status, body, err := execHTTPGet(smokeWorkloadNamespace, smokeClusterName+"-0", "/cluster")
			g.Expect(err).NotTo(HaveOccurred(), "exec HTTP GET 실패:\n%s", body)
			g.Expect(status).To(Equal(200), "GET /cluster 상태코드 불일치: got=%d\n%s", status, body)
			peerCount := strings.Count(body, `"uri":`)
			g.Expect(peerCount).To(Equal(2), "Raft peer 수 불일치: got=%d\n%s", peerCount, body)
		}, 3*time.Minute, 5*time.Second).Should(Succeed())
	})

	It("6. QdrantCluster CR을 삭제한다", func() {
		By("kubectl delete qdrantcluster " + smokeClusterName + " -n " + smokeWorkloadNamespace)
		cmd := exec.Command("kubectl", "delete", "qdrantcluster", smokeClusterName,
			"-n", smokeWorkloadNamespace, "--timeout=2m")
		output, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "QdrantCluster 삭제 실패:\n%s", output)
	})

	It("7. PVC가 Bound 상태로 잔존함을 확인한다(retentionPolicy=Retain)", func() {
		// StatefulSet은 QdrantCluster를 controller owner로 갖고 있어 CR 삭제 시 함께 GC되지만
		// (controller.go의 applyOwned/SetControllerReference), volumeClaimTemplates가 만든
		// PVC는 STS가 소유하지 않는다 — k8s 기본 동작(persistentVolumeClaimRetentionPolicy
		// 미설정 시 whenDeleted=Retain과 동치)이 STS 삭제와 무관하게 PVC를 보존한다. CRD의
		// spec.persistence.retentionPolicy 필드(현재 Phase A에서는 스키마만 있고 STS 빌더가
		// 아직 wiring하지 않음)와 이름은 같지만, 본 검증은 그 필드 값이 아니라 "PVC는
		// StatefulSet이 소유하지 않는다"는 설계(§7 데이터 안전)가 실제로 지켜지는지를 본다.
		for _, ordinal := range []int{0, 1} {
			pvcName := fmt.Sprintf("%s-%s-%d", resources.StorageVolumeName, smokeClusterName, ordinal)
			By(fmt.Sprintf("kubectl get pvc %s -n %s -o jsonpath={.status.phase} == Bound", pvcName, smokeWorkloadNamespace))
			cmd := exec.Command("kubectl", "get", "pvc", pvcName,
				"-n", smokeWorkloadNamespace, "-o", "jsonpath={.status.phase}")
			output, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(),
				"PVC %s 조회 실패 — retentionPolicy=Retain 위반 가능성(CR 삭제와 함께 GC됨):\n%s", pvcName, output)
			Expect(output).To(Equal("Bound"), "PVC %s phase가 Bound가 아님: %s", pvcName, output)
		}
	})
})

// waitClusterPhase는 kubectl get qdrantcluster -o jsonpath='{.status.phase}' 결과가 want와
// 같아질 때까지 재시도하는 Eventually 콜백을 만든다(3/5단계 공용).
func waitClusterPhase(name, want string) func(Gomega) {
	return func(g Gomega) {
		cmd := exec.Command("kubectl", "get", "qdrantcluster", name,
			"-n", smokeWorkloadNamespace, "-o", "jsonpath={.status.phase}")
		output, err := utils.Run(cmd)
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(output).To(Equal(want), "QdrantCluster status.phase 불일치: got=%q want=%q", output, want)
	}
}

// execHTTPGet은 대상 파드의 qdrant 컨테이너 안에서 bash 내장 /dev/tcp 의사 디바이스로 raw
// HTTP/1.1 GET을 보내고 상태 코드 + 원본 응답(헤더+바디)을 반환한다.
//
// 공식 qdrant 이미지(v1.18.2, debian-13-slim 기반)는 curl/wget을 포함하지 않는다 —
// Dockerfile 최종 stage가 `ca-certificates tzdata libunwind8`만 설치한다(2026-07-20 실측,
// github.com/qdrant/qdrant Dockerfile). 반면 STS 컨테이너의 Command 자체가 이미
// "/bin/bash -c ./config/initialize.sh"(internal/resources/statefulset.go)라 bash 존재는
// 보장돼 있으므로, curl 대신 그 bash로 raw TCP 소켓을 열어 HTTP를 직접 구사한다.
func execHTTPGet(namespace, pod, path string) (statusCode int, rawResponse string, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	script := fmt.Sprintf(
		"set -eu\n"+
			"exec 3<>/dev/tcp/127.0.0.1/%d\n"+
			"printf 'GET %s HTTP/1.1\\r\\nHost: localhost\\r\\nConnection: close\\r\\n\\r\\n' >&3\n"+
			"cat <&3\n",
		resources.RESTPort, path,
	)
	cmd := exec.CommandContext(ctx, "kubectl", "exec", "-n", namespace, pod, "-c", "qdrant", "--",
		"/bin/bash", "-c", script)
	out, runErr := utils.Run(cmd)
	if runErr != nil {
		return 0, out, runErr
	}
	statusCode, err = parseHTTPStatusLine(out)
	return statusCode, out, err
}

// parseHTTPStatusLine은 raw HTTP 응답의 첫 줄("HTTP/1.1 200 OK")에서 상태 코드를 뽑는다.
func parseHTTPStatusLine(raw string) (int, error) {
	firstLine, _, found := strings.Cut(raw, "\r\n")
	if !found {
		firstLine = raw
	}
	fields := strings.Fields(firstLine)
	if len(fields) < 2 {
		return 0, fmt.Errorf("HTTP 상태줄 파싱 실패: %q", firstLine)
	}
	return strconv.Atoi(fields[1])
}
