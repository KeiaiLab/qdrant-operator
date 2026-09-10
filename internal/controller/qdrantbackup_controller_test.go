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

var _ = Describe("QdrantBackup 스냅샷 백업 (v0.10.0)", func() {
	makeReady := func(name string, replicas int32) {
		sts := &appsv1.StatefulSet{}
		Eventually(func() error {
			return k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: "default"}, sts)
		}, "10s", "250ms").Should(Succeed())
		sts.Status.Replicas = replicas
		sts.Status.ReadyReplicas = replicas
		Expect(k8sClient.Status().Update(ctx, sts)).To(Succeed())
	}

	It("전 peer 에 스냅샷을 하나씩 만들고 보존기간을 지킨다", func() {
		// peer ID 대역 분리(rf1 주석 참조) — 잔존 컬렉션이 이 스펙에 끼어들지 않게 한다.
		fakeQdrant.SetPeers(
			qdrant.Peer{ID: 81, URI: "http://bk1-0.bk1-headless:6335/"},
			qdrant.Peer{ID: 82, URI: "http://bk1-1.bk1-headless:6335/"},
		)
		// 스냅샷은 노드 단위 — peer 별 Fake 각각에 컬렉션이 존재해야 한다.
		for _, ordinal := range []int32{0, 1} {
			peerFake(ordinal).SetCollection("bkvec", qdrant.CollectionInfo{Exists: true})
		}

		cluster := &qdrantv1alpha1.QdrantCluster{ObjectMeta: metav1.ObjectMeta{Name: "bk1", Namespace: "default"}}
		cluster.Spec.Replicas = 2
		Expect(k8sClient.Create(ctx, cluster)).To(Succeed())
		makeReady("bk1", 2)

		bk := &qdrantv1alpha1.QdrantBackup{ObjectMeta: metav1.ObjectMeta{Name: "bk1-nightly", Namespace: "default"}}
		bk.Spec.ClusterRef = "bk1"
		bk.Spec.Collections = []string{"bkvec"} // 전역 Fake 의 타 스펙 컬렉션 간섭 배제
		Expect(k8sClient.Create(ctx, bk)).To(Succeed())

		// ① 1회성 백업이 즉시 돌아 peer 마다 스냅샷 하나씩.
		key := types.NamespacedName{Name: "bk1-nightly", Namespace: "default"}
		fetched := &qdrantv1alpha1.QdrantBackup{}
		Eventually(func() int {
			_ = k8sClient.Get(ctx, key, fetched)
			return len(fetched.Status.Snapshots)
		}, "30s", "250ms").Should(Equal(2), "peer 2대 × 컬렉션 1개")

		peers := map[int32]bool{}
		for _, s := range fetched.Status.Snapshots {
			Expect(s.Collection).To(Equal("bkvec"))
			peers[s.Peer] = true
		}
		Expect(peers).To(HaveLen(2), "한 peer 에서만 뜨면 노드 단위 팬아웃이 아니다")

		// ② 세대가 끝나면 Ready 로 정착하고 lane·큐가 비어 있다.
		Eventually(func() string {
			_ = k8sClient.Get(ctx, key, fetched)
			return fetched.Status.Phase
		}, "20s", "250ms").Should(Equal(phaseBackupReady))
		Expect(fetched.Status.Active).To(BeNil())
		Expect(fetched.Status.Pending).To(BeEmpty())
		Expect(fetched.Status.LastSuccessTime).NotTo(BeNil())

		// ③ 1회성은 다시 돌지 않는다 — 스냅샷이 무한정 쌓이지 않는다.
		Consistently(func() int {
			_ = k8sClient.Get(ctx, key, fetched)
			return len(fetched.Status.Snapshots)
		}, "3s", "500ms").Should(Equal(2))
	})

	It("retention 미지정이면 아무것도 지우지 않는다", func() {
		// 보존기간을 선언하지 않은 백업이 스냅샷을 지우면 그것은 데이터 손실이다.
		for _, ordinal := range []int32{0, 1} {
			f := peerFake(ordinal)
			f.SetCollection("keepvec", qdrant.CollectionInfo{Exists: true})
			// 이전에 손으로 만들어 둔 스냅샷 3개.
			for range 3 {
				Expect(f.CreateSnapshot(ctx, "keepvec")).To(Succeed())
			}
		}

		cluster := &qdrantv1alpha1.QdrantCluster{ObjectMeta: metav1.ObjectMeta{Name: "bk2", Namespace: "default"}}
		cluster.Spec.Replicas = 2
		Expect(k8sClient.Create(ctx, cluster)).To(Succeed())
		makeReady("bk2", 2)

		bk := &qdrantv1alpha1.QdrantBackup{ObjectMeta: metav1.ObjectMeta{Name: "bk2-once", Namespace: "default"}}
		bk.Spec.ClusterRef = "bk2"
		bk.Spec.Collections = []string{"keepvec"}
		Expect(k8sClient.Create(ctx, bk)).To(Succeed())

		key := types.NamespacedName{Name: "bk2-once", Namespace: "default"}
		fetched := &qdrantv1alpha1.QdrantBackup{}
		Eventually(func() string {
			_ = k8sClient.Get(ctx, key, fetched)
			return fetched.Status.Phase
		}, "30s", "250ms").Should(Equal(phaseBackupReady))

		// 기존 3 + 새로 만든 1 = 4. 하나도 지워지지 않아야 한다.
		for _, ordinal := range []int32{0, 1} {
			list, err := peerFake(ordinal).ListSnapshots(ctx, "keepvec")
			Expect(err).NotTo(HaveOccurred())
			Expect(list).To(HaveLen(4), "retention 미지정인데 스냅샷이 지워졌다")
		}
	})

	It("클러스터가 없으면 Degraded 로 표면화한다", func() {
		bk := &qdrantv1alpha1.QdrantBackup{ObjectMeta: metav1.ObjectMeta{Name: "bk-orphan", Namespace: "default"}}
		bk.Spec.ClusterRef = "없는클러스터"
		Expect(k8sClient.Create(ctx, bk)).To(Succeed())

		key := types.NamespacedName{Name: "bk-orphan", Namespace: "default"}
		fetched := &qdrantv1alpha1.QdrantBackup{}
		Eventually(func() string {
			_ = k8sClient.Get(ctx, key, fetched)
			for _, c := range fetched.Status.Conditions {
				if c.Type == condDegraded && c.Status == metav1.ConditionTrue {
					return c.Reason
				}
			}
			return ""
		}, "20s", "250ms").Should(Equal("ClusterNotFound"))
	})

	It("cron 표기가 틀리면 조용히 멈추지 않고 Degraded 가 된다", func() {
		bk := &qdrantv1alpha1.QdrantBackup{ObjectMeta: metav1.ObjectMeta{Name: "bk-badcron", Namespace: "default"}}
		bk.Spec.ClusterRef = "bk1"
		bk.Spec.Schedule = "매일 새벽 세시"
		Expect(k8sClient.Create(ctx, bk)).To(Succeed())

		key := types.NamespacedName{Name: "bk-badcron", Namespace: "default"}
		fetched := &qdrantv1alpha1.QdrantBackup{}
		Eventually(func() string {
			_ = k8sClient.Get(ctx, key, fetched)
			for _, c := range fetched.Status.Conditions {
				if c.Type == condDegraded && c.Status == metav1.ConditionTrue {
					return c.Reason
				}
			}
			return ""
		}, "20s", "250ms").Should(Equal(reasonScheduleValid))
		Expect(fetched.Status.LastSuccessTime).To(BeNil(), "잘못된 예약으로 백업이 돌면 안 된다")
	})
})
