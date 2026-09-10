/*
Copyright 2026 Keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package controller

import (
	"testing"

	appsv1 "k8s.io/api/apps/v1"

	qdrantv1alpha1 "github.com/keiailab/qdrant-operator/api/v1alpha1"
	"github.com/keiailab/qdrant-operator/internal/qdrant"
)

// rolloutSTS 는 롤아웃 상태의 STS 를 만든다.
func rolloutSTS(updated, ready int32, inProgress bool) *appsv1.StatefulSet {
	sts := &appsv1.StatefulSet{}
	sts.Status.CurrentRevision = "rev-1"
	sts.Status.UpdateRevision = "rev-1"
	if inProgress {
		sts.Status.UpdateRevision = "rev-2"
	}
	sts.Status.UpdatedReplicas = updated
	sts.Status.ReadyReplicas = ready
	return sts
}

// healthyObs 는 peer n 대가 전부 합의에 있고 shard 가 전부 Active 인 관측이다.
func healthyObs(n int) *observation {
	peers := make([]uint64, 0, n)
	for i := range n {
		peers = append(peers, uint64(i+1))
	}
	placement := map[string]map[uint32][]uint64{"c": {0: peers}}
	return obs(peers, placement)
}

func TestUpgradePartition_한칸씩내려간다(t *testing.T) {
	o := healthyObs(3)

	// 롤아웃 전 — 렌더 산출물이 기존과 같아야 한다(무행동 불변).
	if got := upgradePartition(rolloutSTS(0, 3, false), 3, o); got != 0 {
		t.Fatalf("롤아웃 없음인데 partition=%d", got)
	}

	// 시작: 가장 높은 서수만 갱신 대상.
	if got := upgradePartition(rolloutSTS(0, 3, true), 3, o); got != 2 {
		t.Fatalf("첫 걸음 partition=%d (want 2)", got)
	}
	// 한 대가 갱신되고 건강 → 다음 서수.
	if got := upgradePartition(rolloutSTS(1, 3, true), 3, o); got != 1 {
		t.Fatalf("두 번째 걸음 partition=%d (want 1)", got)
	}
	// 마지막 한 대 남음 → 0 까지 내려간다.
	if got := upgradePartition(rolloutSTS(2, 3, true), 3, o); got != 0 {
		t.Fatalf("마지막 걸음 partition=%d (want 0)", got)
	}
	// 전부 갱신 — 0 에 머문다.
	if got := upgradePartition(rolloutSTS(3, 3, true), 3, o); got != 0 {
		t.Fatalf("완료 partition=%d", got)
	}
}

func TestUpgradePartition_건강하지않으면동결(t *testing.T) {
	// 파드는 떴는데 shard 가 아직 전이 중 — 기본 롤링이 다음 파드를 내려버리는 바로 그 순간이다.
	o := healthyObs(3)
	setState(o, 0, 1, "Initializing")
	if got := upgradePartition(rolloutSTS(1, 3, true), 3, o); got != 2 {
		t.Fatalf("전이 중인데 진행: partition=%d (want 2 동결)", got)
	}

	// 전송이 남아 있어도 동결.
	o2 := healthyObs(3)
	o2.Collections["c"].Transfers = []qdrant.TransferInfo{{ShardID: 0, From: 1, To: 2}}
	if got := upgradePartition(rolloutSTS(1, 3, true), 3, o2); got != 2 {
		t.Fatalf("전송 중인데 진행: partition=%d", got)
	}

	// peer 가 아직 합의에 돌아오지 않았으면 동결.
	if got := upgradePartition(rolloutSTS(1, 3, true), 3, healthyObs(2)); got != 2 {
		t.Fatalf("합의 미복귀인데 진행: partition=%d", got)
	}

	// 파드가 Ready 가 아니면 동결.
	if got := upgradePartition(rolloutSTS(1, 2, true), 3, healthyObs(3)); got != 2 {
		t.Fatalf("Ready 미달인데 진행: partition=%d", got)
	}

	// 관측 실패(nil)면 진행하지 않는다 — 모르면 멈춘다.
	if got := upgradePartition(rolloutSTS(1, 3, true), 3, nil); got != 2 {
		t.Fatalf("관측 실패인데 진행: partition=%d", got)
	}
}

func TestUpgradeReady_Dead는막지않는다(t *testing.T) {
	// Dead replica 는 재복제가 고칠 일이다. 그것으로 업그레이드를 잠그면 리밸런스에서
	// 겪은 교착을 여기서 되풀이한다.
	o := healthyObs(3)
	setState(o, 0, 1, qdrant.ShardStateDead)
	if !upgradeReady(rolloutSTS(1, 3, true), 3, o) {
		t.Fatal("Dead 가 업그레이드를 막았다")
	}
}

func TestTLS_설정판정(t *testing.T) {
	qc := &qdrantv1alpha1.QdrantCluster{}
	qc.Namespace, qc.Name = "data", "q"

	// 기본 — TLS 아님. 기존 클러스터가 여기에 해당한다.
	if tlsEnabled(qc) || tlsMisconfigured(qc) {
		t.Fatal("기본값이 TLS 로 판정됨")
	}
	if got := clientBaseURL(qc); got != "http://q.data.svc:6333" {
		t.Fatalf("평문 주소: %s", got)
	}

	// 플래그만 켠 상태 = TLS 가 아니라 깨진 설정이다.
	qc.Spec.Config.TLSEnabled = true
	if tlsEnabled(qc) {
		t.Fatal("인증서 없이 TLS 로 판정됨")
	}
	if !tlsMisconfigured(qc) {
		t.Fatal("깨진 설정을 잡지 못함")
	}

	// 인증서까지 갖춰야 비로소 https 다.
	qc.Spec.Config.TLS = &qdrantv1alpha1.TLSSpec{SecretName: "certs"}
	if !tlsEnabled(qc) || tlsMisconfigured(qc) {
		t.Fatal("완전한 설정을 TLS 로 인정하지 않음")
	}
	if got := clientBaseURL(qc); got != "https://q.data.svc:6333" {
		t.Fatalf("client 주소: %s", got)
	}
	if got := peerBaseURL(qc, 2); got != "https://q-2.q-headless.data.svc:6333" {
		t.Fatalf("peer 주소: %s", got)
	}
}
