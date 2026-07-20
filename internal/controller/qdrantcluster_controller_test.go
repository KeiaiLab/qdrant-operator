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

package controller

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	qdrantv1alpha1 "github.com/keiailab/qdrant-operator/api/v1alpha1"
	"github.com/keiailab/qdrant-operator/internal/qdrant"
)

var _ = Describe("QdrantCluster Controller", func() {
	// spec을 비운 채 생성해도 kubebuilder default 마커가 CRD를 통해 채워지는지 검증
	Context("When creating a resource with an empty spec", func() {
		It("적용 시 spec default가 채워진다", func() {
			qc := &qdrantv1alpha1.QdrantCluster{
				ObjectMeta: metav1.ObjectMeta{Name: "def", Namespace: "default"},
			}
			Expect(k8sClient.Create(ctx, qc)).To(Succeed())
			fetched := &qdrantv1alpha1.QdrantCluster{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "def", Namespace: "default"}, fetched)).To(Succeed())
			Expect(fetched.Spec.Replicas).To(Equal(int32(1)))
			Expect(fetched.Spec.Image.Tag).To(Equal("v1.18.2"))
			Expect(fetched.Spec.Persistence.StorageClassName).To(Equal("ceph-rbd"))
			Expect(fetched.Spec.RunAsUser).To(Equal(int64(1000)))
			Expect(fetched.Spec.Persistence.Size).NotTo(BeNil()) // 리뷰 #1 회귀 가드
			Expect(fetched.Spec.Persistence.Size.String()).To(Equal("10Gi"))

			By("Cleanup the specific resource instance QdrantCluster")
			Expect(k8sClient.Delete(ctx, fetched)).To(Succeed())
		})
	})

	Context("QdrantCluster child 리소스 reconcile (Task 7)", func() {
		It("reconcile 시 5 child가 ownerRef와 함께 생성된다", func() {
			qc := &qdrantv1alpha1.QdrantCluster{ObjectMeta: metav1.ObjectMeta{Name: "c1", Namespace: "default"}}
			Expect(k8sClient.Create(ctx, qc)).To(Succeed())

			sts := &appsv1.StatefulSet{}
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{Name: "c1", Namespace: "default"}, sts)
			}, "10s", "250ms").Should(Succeed())
			Expect(metav1.GetControllerOf(sts).Kind).To(Equal("QdrantCluster"))

			for _, n := range []string{"c1", "c1-headless"} {
				svc := &corev1.Service{}
				Expect(k8sClient.Get(ctx, types.NamespacedName{Name: n, Namespace: "default"}, svc)).To(Succeed())
				Expect(metav1.GetControllerOf(svc)).NotTo(BeNil())
			}
		})

		It("두 번째 reconcile은 중복 생성 없이 no-op이다", func() {
			// 비동기 매니저의 우연한 실행 횟수에 기대지 않고, 위 It이 만든 "c1"에 대해
			// 명시적으로 2차 reconcile을 실행 — applyOwned의 Update 분기(기존 리소스 발견 시
			// ResourceVersion을 채워 Update)가 충돌·중복 없이 멱등함을 직접 증명한다.
			reconciler := &QdrantClusterReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: "c1", Namespace: "default"},
			})
			Expect(err).NotTo(HaveOccurred())

			// app.kubernetes.io/instance=c1 라벨로 스코프 — envtest는 GC 컨트롤러를 돌리지 않아
			// 같은 namespace(default)의 다른 It(test-resource/def)이 남긴 child와 섞이지 않도록 함.
			list := &appsv1.StatefulSetList{}
			Expect(k8sClient.List(ctx, list, client.InNamespace("default"), client.MatchingLabels{"app.kubernetes.io/instance": "c1"})).To(Succeed())
			Expect(list.Items).To(HaveLen(1))
		})
	})

	Context("QdrantCluster status reconcile (Task 8)", func() {
		// 다른 It("c1"/"test-resource"/"def")과 이름이 겹치면 envtest가 GC를 돌리지 않아
		// 잔존 리소스와 순서 결합이 생기므로, 본 spec 전용 이름(st8)으로 자기 완결 실행한다.
		It("status.phase와 Progressing condition이 설정된다", func() {
			key := types.NamespacedName{Name: "st8", Namespace: "default"}
			qc := &qdrantv1alpha1.QdrantCluster{ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace}}
			Expect(k8sClient.Create(ctx, qc)).To(Succeed())

			fetched := &qdrantv1alpha1.QdrantCluster{}
			Eventually(func() string {
				_ = k8sClient.Get(ctx, key, fetched)
				return fetched.Status.Phase
			}, "10s", "250ms").ShouldNot(BeEmpty())

			// envtest에는 kubelet이 없어 STS의 ReadyReplicas가 항상 0으로 남는다 —
			// 즉 phase는 절대 Running에 도달하지 못하고 Progressing=True로 고정된다.
			Expect(meta.FindStatusCondition(fetched.Status.Conditions, "Progressing")).NotTo(BeNil())
		})
	})

	Context("STS immutable 필드 변경 가드 (Task 9)", func() {
		// 다른 It과 이름이 겹치면 envtest가 GC를 돌리지 않아 잔존 리소스와 순서 결합이
		// 생기므로, 본 spec 전용 이름(im9)으로 자기 완결 실행한다.
		It("persistence.size 변경은 crash 없이 Degraded로 표면화된다", func() {
			key := types.NamespacedName{Name: "im9", Namespace: "default"}
			qc := &qdrantv1alpha1.QdrantCluster{ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace}}
			Expect(k8sClient.Create(ctx, qc)).To(Succeed())

			// 매니저의 최초 reconcile이 STS를 만들 때까지 대기 (수동 Reconcile 호출 금지 — 매니저와 경합 방지)
			sts := &appsv1.StatefulSet{}
			Eventually(func() error {
				return k8sClient.Get(ctx, key, sts)
			}, "10s", "250ms").Should(Succeed())

			fetched := &qdrantv1alpha1.QdrantCluster{}
			Expect(k8sClient.Get(ctx, key, fetched)).To(Succeed())
			twentyGi := resource.MustParse("20Gi")
			fetched.Spec.Persistence.Size = &twentyGi
			Expect(k8sClient.Update(ctx, fetched)).To(Succeed())

			Eventually(func() *metav1.Condition {
				_ = k8sClient.Get(ctx, key, fetched)
				return meta.FindStatusCondition(fetched.Status.Conditions, "Degraded")
			}, "10s", "250ms").ShouldNot(BeNil())
		})
	})

	Context("STS scale-down 거부 가드 (Task 10)", func() {
		// 다른 It과 이름이 겹치면 envtest가 GC를 돌리지 않아 잔존 리소스와 순서 결합이
		// 생기므로, 본 spec 전용 이름(sd10)으로 자기 완결 실행한다.
		It("scale-down은 거부되고 STS replica는 유지된다", func() {
			key := types.NamespacedName{Name: "sd10", Namespace: "default"}
			qc := &qdrantv1alpha1.QdrantCluster{ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace}}
			qc.Spec.Replicas = 3
			Expect(k8sClient.Create(ctx, qc)).To(Succeed())

			// 매니저의 최초 reconcile이 STS를 replicas=3으로 만들 때까지 대기 (수동 Reconcile 호출 금지 — 매니저와 경합 방지)
			Eventually(func() int32 {
				s := &appsv1.StatefulSet{}
				_ = k8sClient.Get(ctx, key, s)
				if s.Spec.Replicas == nil {
					return 0
				}
				return *s.Spec.Replicas
			}, "10s", "250ms").Should(Equal(int32(3)))

			fetched := &qdrantv1alpha1.QdrantCluster{}
			Expect(k8sClient.Get(ctx, key, fetched)).To(Succeed())
			fetched.Spec.Replicas = 1
			Expect(k8sClient.Update(ctx, fetched)).To(Succeed())

			// envtest에는 kubelet이 없어 STS의 status는 항상 비어있다 — 가드가 읽는 것은
			// liveSTS.Spec.Replicas이므로 spec을 비교한다.
			Consistently(func() int32 {
				s := &appsv1.StatefulSet{}
				_ = k8sClient.Get(ctx, key, s)
				if s.Spec.Replicas == nil {
					return 0
				}
				return *s.Spec.Replicas
			}, "3s", "500ms").Should(Equal(int32(3))) // 거부되어 3 유지

			Eventually(func() *metav1.Condition {
				_ = k8sClient.Get(ctx, key, fetched)
				return meta.FindStatusCondition(fetched.Status.Conditions, "Degraded")
			}, "10s", "250ms").ShouldNot(BeNil())
		})
	})

	Context("PVC 미소유 불변식 검증 (Task 11)", func() {
		// 다른 It과 이름이 겹치면 envtest가 GC를 돌리지 않아 잔존 리소스와 순서 결합이
		// 생기므로, 본 spec 전용 이름(pvc11)으로 자기 완결 실행한다.
		It("PVC는 QdrantCluster의 controller-ref를 갖지 않는다(데이터 안전)", func() {
			key := types.NamespacedName{Name: "pvc11", Namespace: "default"}
			qc := &qdrantv1alpha1.QdrantCluster{ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace}}
			Expect(k8sClient.Create(ctx, qc)).To(Succeed())

			// 매니저의 최초 reconcile이 STS를 만들 때까지 대기 (수동 Reconcile 호출 금지 — 매니저와 경합 방지)
			sts := &appsv1.StatefulSet{}
			Eventually(func() error {
				return k8sClient.Get(ctx, key, sts)
			}, "10s", "250ms").Should(Succeed())

			// STS의 volumeClaimTemplates는 STS가 소유하거나 무소유 — QdrantCluster 직접 소유 금지
			// CR 삭제 시 GC가 PVC를 함께 삭제하는 것을 방지 (데이터 안전 설계 §7)
			for _, vct := range sts.Spec.VolumeClaimTemplates {
				for _, ownerRef := range vct.OwnerReferences {
					Expect(ownerRef.Kind).NotTo(Equal("QdrantCluster"),
						"PVC 템플릿이 QdrantCluster controller-ref를 가지면 CR 삭제 시 데이터 유실 위험")
				}
			}

			// STS 자체는 QdrantCluster의 controller-ref를 가져야 함 (대조군 — STS는 CR 삭제 시 함께 삭제됨)
			Expect(metav1.GetControllerOf(sts).Kind).To(Equal("QdrantCluster"))
		})
	})
})

// B-2 분포 관측 — fake 에 2-peer 배치를 주입하면 status.shardDistribution 으로 보고된다.
var _ = Describe("QdrantCluster 분포 관측 (B-2)", func() {
	It("컬렉션별 peer 간 shard 분포를 status 로 보고한다", func() {
		fakeQdrant.SetPeers(
			qdrant.Peer{ID: 11, URI: "http://obs2-0.obs2-headless:6335/"},
			qdrant.Peer{ID: 22, URI: "http://obs2-1.obs2-headless:6335/"},
		)
		fakeQdrant.SetCollection("obsvec", qdrant.CollectionInfo{
			Exists: true, VectorSize: 384, Distance: "Cosine", ShardNumber: 3, ReplicationFactor: 1,
		})
		fakeQdrant.SetPlacement("obsvec", map[uint32]uint64{0: 11, 1: 11, 2: 22})

		Expect(k8sClient.Create(ctx, &qdrantv1alpha1.QdrantCluster{
			ObjectMeta: metav1.ObjectMeta{Name: "obs2", Namespace: "default"},
		})).To(Succeed())

		fetched := &qdrantv1alpha1.QdrantCluster{}
		Eventually(func() int {
			_ = k8sClient.Get(ctx, types.NamespacedName{Name: "obs2", Namespace: "default"}, fetched)
			return len(fetched.Status.ShardDistribution)
		}, "10s", "250ms").ShouldNot(BeZero())

		var obsvec *qdrantv1alpha1.CollectionDistribution
		for i := range fetched.Status.ShardDistribution {
			if fetched.Status.ShardDistribution[i].Collection == "obsvec" {
				obsvec = &fetched.Status.ShardDistribution[i]
			}
		}
		Expect(obsvec).NotTo(BeNil(), "obsvec 분포가 보고돼야 함")
		Expect(obsvec.PerPeer).To(HaveLen(2))
		Expect(obsvec.PerPeer[0].Peer).To(Equal("11"))
		Expect(obsvec.PerPeer[0].Shards).To(Equal(int32(2)))
		Expect(obsvec.PerPeer[1].Peer).To(Equal("22"))
		Expect(obsvec.PerPeer[1].Shards).To(Equal(int32(1)))
		Expect(obsvec.TransfersInFlight).To(Equal(int32(0)))
		Expect(fetched.Status.Peers).To(Equal([]string{"11", "22"}))
	})
})

// B-3 rebalance — envtest 에는 kubelet 이 없어 STS readyReplicas 가 스스로 오르지 않으므로
// 테스트가 STS status 를 직접 패치해 ready 게이트를 연다(표준 envtest 트릭 — STS 컨트롤러
// 부재라 패치 값이 유지되고, Owns(STS) watch 가 재-reconcile 을 트리거한다).
var _ = Describe("QdrantCluster rebalance (B-3)", func() {
	makeReady := func(name string, replicas int32) {
		sts := &appsv1.StatefulSet{}
		Eventually(func() error {
			return k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: "default"}, sts)
		}, "10s", "250ms").Should(Succeed())
		sts.Status.Replicas = replicas
		sts.Status.ReadyReplicas = replicas
		Expect(k8sClient.Status().Update(ctx, sts)).To(Succeed())
	}

	It("불균형을 계획 선노출 후 동시 1건 이동으로 수렴시킨다", func() {
		fakeQdrant.SetPeers(
			qdrant.Peer{ID: 31, URI: "http://reb3-0.reb3-headless:6335/"},
			qdrant.Peer{ID: 32, URI: "http://reb3-1.reb3-headless:6335/"},
		)
		fakeQdrant.SetCollection("rebvec", qdrant.CollectionInfo{
			Exists: true, VectorSize: 384, Distance: "Cosine", ShardNumber: 3, ReplicationFactor: 1,
		})
		fakeQdrant.SetPlacement("rebvec", map[uint32]uint64{0: 31, 1: 31, 2: 31}) // 3:0 불균형

		qc := &qdrantv1alpha1.QdrantCluster{ObjectMeta: metav1.ObjectMeta{Name: "reb3", Namespace: "default"}}
		qc.Spec.Replicas = 2
		Expect(k8sClient.Create(ctx, qc)).To(Succeed())
		makeReady("reb3", 2)

		// 수렴: 이동이 발행되고(동시 1건) 배치가 균형(2:1)에 도달, phase 는 Running 복귀.
		Eventually(func() bool {
			pl := fakeQdrant.GetPlacement("rebvec")
			count := map[uint64]int{}
			for _, p := range pl {
				count[p]++
			}
			return count[31] == 2 && count[32] == 1
		}, "20s", "250ms").Should(BeTrue(), "3:0 → 2:1 수렴해야 함")
		Expect(fakeQdrant.Moves).To(HaveLen(1), "필요 이동은 정확히 1건(동시 1건·최소 이동)")
		Expect(fakeQdrant.Moves[0]).To(Equal("rebvec/0:31->32"), "결정론 첫 이동")

		fetched := &qdrantv1alpha1.QdrantCluster{}
		Eventually(func() string {
			_ = k8sClient.Get(ctx, types.NamespacedName{Name: "reb3", Namespace: "default"}, fetched)
			return fetched.Status.Phase
		}, "15s", "250ms").Should(Equal("Running"))
		Expect(fetched.Status.PlannedMoves).To(BeEmpty(), "균형 후 계획은 비어야 함")
		Expect(fetched.Status.Rebalance).To(BeNil(), "완료 후 발행 추적은 정산돼야 함")
	})

	It("dry-run(enabled=false)은 계획만 노출하고 이동을 발행하지 않는다", func() {
		fakeQdrant.SetPeers(
			qdrant.Peer{ID: 41, URI: "http://reb3d-0.reb3d-headless:6335/"},
			qdrant.Peer{ID: 42, URI: "http://reb3d-1.reb3d-headless:6335/"},
		)
		fakeQdrant.SetCollection("dryvec", qdrant.CollectionInfo{
			Exists: true, VectorSize: 384, Distance: "Cosine", ShardNumber: 2, ReplicationFactor: 1,
		})
		fakeQdrant.SetPlacement("dryvec", map[uint32]uint64{0: 41, 1: 41}) // 2:0 불균형

		off := false
		qc := &qdrantv1alpha1.QdrantCluster{ObjectMeta: metav1.ObjectMeta{Name: "reb3d", Namespace: "default"}}
		qc.Spec.Replicas = 2
		qc.Spec.Rebalance = &qdrantv1alpha1.RebalanceSpec{Enabled: &off}
		Expect(k8sClient.Create(ctx, qc)).To(Succeed())
		makeReady("reb3d", 2)

		fetched := &qdrantv1alpha1.QdrantCluster{}
		Eventually(func() []string {
			_ = k8sClient.Get(ctx, types.NamespacedName{Name: "reb3d", Namespace: "default"}, fetched)
			return fetched.Status.PlannedMoves
		}, "15s", "250ms").Should(ContainElement("dryvec/0: 41->42"), "dry-run 도 계획은 노출")
		Consistently(func() []string {
			return fakeQdrant.Moves
		}, "3s", "500ms").ShouldNot(ContainElement(ContainSubstring("dryvec")), "dry-run 은 발행 금지")
		Expect(fetched.Status.Phase).To(Equal("Running"))
	})
})
