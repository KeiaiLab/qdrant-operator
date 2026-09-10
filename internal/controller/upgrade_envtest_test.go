/*
Copyright 2026 Keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package controller

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	qdrantv1alpha1 "github.com/keiailab/qdrant-operator/api/v1alpha1"
	"github.com/keiailab/qdrant-operator/internal/qdrant"
)

var _ = Describe("QdrantCluster Raft-aware 롤링 업그레이드 (v0.10.0)", func() {
	key := types.NamespacedName{Name: "up1", Namespace: "default"}

	// envtest 에는 StatefulSet 컨트롤러가 없다 — 롤아웃 상태를 직접 만든다.
	setRollout := func(updated, ready int32) {
		sts := &appsv1.StatefulSet{}
		Eventually(func() error { return k8sClient.Get(ctx, key, sts) }, "10s", "250ms").Should(Succeed())
		sts.Status.Replicas = 3
		sts.Status.ReadyReplicas = ready
		sts.Status.UpdatedReplicas = updated
		sts.Status.CurrentRevision = "rev-1"
		sts.Status.UpdateRevision = "rev-2"
		Expect(k8sClient.Status().Update(ctx, sts)).To(Succeed())
	}

	partition := func() int32 {
		sts := &appsv1.StatefulSet{}
		if err := k8sClient.Get(ctx, key, sts); err != nil {
			return -1
		}
		if sts.Spec.UpdateStrategy.RollingUpdate == nil || sts.Spec.UpdateStrategy.RollingUpdate.Partition == nil {
			return -1
		}
		return *sts.Spec.UpdateStrategy.RollingUpdate.Partition
	}

	It("클러스터가 건강할 때만 다음 서수로 내려간다", func() {
		fakeQdrant.SetPeers(
			qdrant.Peer{ID: 101, URI: "http://up1-0.up1-headless:6335/"},
			qdrant.Peer{ID: 102, URI: "http://up1-1.up1-headless:6335/"},
			qdrant.Peer{ID: 103, URI: "http://up1-2.up1-headless:6335/"},
		)
		fakeQdrant.SetCollection("upvec", qdrant.CollectionInfo{Exists: true})
		fakeQdrant.SetPlacement("upvec", map[uint32]uint64{0: 101, 1: 102, 2: 103})

		qc := &qdrantv1alpha1.QdrantCluster{ObjectMeta: metav1.ObjectMeta{Name: "up1", Namespace: "default"}}
		qc.Spec.Replicas = 3
		Expect(k8sClient.Create(ctx, qc)).To(Succeed())

		// ① 롤아웃 시작 — 가장 높은 서수만 열린다.
		setRollout(0, 3)
		Eventually(partition, "20s", "250ms").Should(Equal(int32(2)), "첫 걸음은 최고 서수 하나만")

		fetched := &qdrantv1alpha1.QdrantCluster{}
		Eventually(func() string {
			_ = k8sClient.Get(ctx, key, fetched)
			return fetched.Status.Phase
		}, "20s", "250ms").Should(Equal(phaseUpgrading))

		// ② 한 대 갱신됐지만 shard 가 아직 전이 중 — 기본 롤링이라면 다음 파드를 내렸을 자리다.
		fakeQdrant.SetShardState("upvec", 2, 103, "Initializing")
		setRollout(1, 3)
		Consistently(partition, "4s", "500ms").Should(Equal(int32(2)), "전이 중인데 다음 서수를 열었다")

		// ③ shard 가 Active 로 돌아오면 비로소 진행한다.
		fakeQdrant.SetShardState("upvec", 2, 103, qdrant.ShardStateActive)
		setRollout(1, 3)
		Eventually(partition, "20s", "250ms").Should(Equal(int32(1)))

		// ④ 마지막까지 0 으로 내려간다.
		setRollout(2, 3)
		Eventually(partition, "20s", "250ms").Should(Equal(int32(0)))
	})
})
