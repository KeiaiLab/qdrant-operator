/*
Copyright 2026 Keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package controller

import (
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	autoscalingv1 "k8s.io/api/autoscaling/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
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
		// Moves 는 suite 전역 누적이라 본 컬렉션(rebvec) 것만 센다 — 다른 스펙과 실행 순서 무관.
		var rebMoves []string
		for _, m := range fakeQdrant.Moves {
			if strings.HasPrefix(m, "rebvec/") {
				rebMoves = append(rebMoves, m)
			}
		}
		Expect(rebMoves).To(HaveLen(1), "필요 이동은 정확히 1건(동시 1건·최소 이동)")
		Expect(rebMoves[0]).To(Equal("rebvec/0:31->32"), "결정론 첫 이동")

		fetched := &qdrantv1alpha1.QdrantCluster{}
		// phase 전환과 계획 정리는 서로 다른 reconcile 에서 수렴하므로
		// 세 필드를 하나의 Eventually 로 묶어야 레이스가 없다.
		Eventually(func(g Gomega) {
			g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "reb3", Namespace: "default"}, fetched)).To(Succeed())
			g.Expect(fetched.Status.Phase).To(Equal("Running"))
			g.Expect(fetched.Status.PlannedMoves).To(BeEmpty(), "균형 후 계획은 비어야 함")
			g.Expect(fetched.Status.ActiveMove).To(BeNil(), "완료 후 발행 추적은 정산돼야 함")
		}, "45s", "250ms").Should(Succeed())
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

// B-4 scale-in drain — 구 거부 가드의 계약 대체: 축소는 거부가 아니라
// 이동 → RemovePeer(force=false) → 1 서수 축소의 안전 절차로 완주한다.
var _ = Describe("QdrantCluster scale-in drain (B-4)", func() {
	It("3→2 축소를 드레인 절차로 완주한다 (이동→peer 제거→축소→정리)", func() {
		fakeQdrant.SetPeers(
			qdrant.Peer{ID: 51, URI: "http://dr4-0.dr4-headless:6335/"},
			qdrant.Peer{ID: 52, URI: "http://dr4-1.dr4-headless:6335/"},
			qdrant.Peer{ID: 53, URI: "http://dr4-2.dr4-headless:6335/"},
		)
		fakeQdrant.SetCollection("drvec", qdrant.CollectionInfo{
			Exists: true, VectorSize: 384, Distance: "Cosine", ShardNumber: 3, ReplicationFactor: 1,
		})
		fakeQdrant.SetPlacement("drvec", map[uint32]uint64{0: 51, 1: 52, 2: 53})

		qc := &qdrantv1alpha1.QdrantCluster{ObjectMeta: metav1.ObjectMeta{Name: "dr4", Namespace: "default"}}
		qc.Spec.Replicas = 3
		Expect(k8sClient.Create(ctx, qc)).To(Succeed())
		sts := &appsv1.StatefulSet{}
		Eventually(func() error {
			return k8sClient.Get(ctx, types.NamespacedName{Name: "dr4", Namespace: "default"}, sts)
		}, "10s", "250ms").Should(Succeed())
		sts.Status.Replicas, sts.Status.ReadyReplicas = 3, 3
		Expect(k8sClient.Status().Update(ctx, sts)).To(Succeed())

		// 균형(1/1/1) 확인 후 축소 지시
		fetched := &qdrantv1alpha1.QdrantCluster{}
		Eventually(func() string {
			_ = k8sClient.Get(ctx, types.NamespacedName{Name: "dr4", Namespace: "default"}, fetched)
			return fetched.Status.Phase
		}, "15s", "250ms").Should(Equal("Running"))
		fetched.Spec.Replicas = 2
		Expect(k8sClient.Update(ctx, fetched)).To(Succeed())

		// ① 대상(서수 2 = peer 53)의 shard 가 최소부하 keeper(동률 → id 오름차순 = 51)로 이동
		Eventually(func() []string { return fakeQdrant.Moves }, "20s", "250ms").
			Should(ContainElement("drvec/2:53->51"))
		// ② 빈 peer 53 이 합의에서 제거되고
		Eventually(func() []uint64 { return fakeQdrant.RemovedPeers }, "20s", "250ms").
			Should(ContainElement(uint64(53)))
		// ③ STS 가 정확히 1 서수 축소된다
		Eventually(func() int32 {
			_ = k8sClient.Get(ctx, types.NamespacedName{Name: "dr4", Namespace: "default"}, sts)
			if sts.Spec.Replicas == nil {
				return -1
			}
			return *sts.Spec.Replicas
		}, "20s", "250ms").Should(Equal(int32(2)))

		// ④ 축소 완료(물리==목표) 후 정상 경로가 DrainStatus 를 정리한다
		sts.Status.Replicas, sts.Status.ReadyReplicas = 2, 2
		Expect(k8sClient.Status().Update(ctx, sts)).To(Succeed())
		Eventually(func() bool {
			_ = k8sClient.Get(ctx, types.NamespacedName{Name: "dr4", Namespace: "default"}, fetched)
			return fetched.Status.DrainStatus == nil && fetched.Status.Phase == "Running"
		}, "20s", "250ms").Should(BeTrue(), "정상 경로 복귀 시 DrainStatus=nil + Running")
	})
})

var _ = Describe("QdrantCluster scale subresource (KEDA 연동)", func() {
	// KEDA ScaledObject 는 /scale 을 정의한 CR 만 스케일할 수 있다 — 설계 keda-autoscale §오퍼레이터 변경.
	It("/scale 로 replicas·selector 를 노출하고 변경을 spec 에 반영한다", func() {
		qc := &qdrantv1alpha1.QdrantCluster{ObjectMeta: metav1.ObjectMeta{Name: "sc1", Namespace: "default"}}
		Expect(k8sClient.Create(ctx, qc)).To(Succeed())

		// GET /scale — 컨트롤러 reconcileStatus 가 status.selector 를 채운 뒤부터 selector 노출.
		scale := &autoscalingv1.Scale{}
		Eventually(func(g Gomega) {
			g.Expect(k8sClient.SubResource("scale").Get(ctx, qc, scale)).To(Succeed())
			g.Expect(scale.Status.Selector).NotTo(BeEmpty())
		}, "10s", "250ms").Should(Succeed())
		Expect(scale.Spec.Replicas).To(Equal(int32(1)))
		Expect(scale.Status.Selector).To(ContainSubstring("app.kubernetes.io/instance=sc1"))

		// PUT /scale — KEDA/HPA 가 쓰는 경로 그대로 replicas 를 2 로 올리면 spec 에 반영된다.
		scale.Spec.Replicas = 2
		Expect(k8sClient.SubResource("scale").Update(ctx, qc, client.WithSubResourceBody(scale))).To(Succeed())
		fetched := &qdrantv1alpha1.QdrantCluster{}
		Eventually(func() int32 {
			_ = k8sClient.Get(ctx, types.NamespacedName{Name: "sc1", Namespace: "default"}, fetched)
			return fetched.Spec.Replicas
		}, "5s", "250ms").Should(Equal(int32(2)))
	})
})

var _ = Describe("QdrantCluster RF 재복제 (v0.4.0)", func() {
	makeReady := func(name string, replicas int32) {
		sts := &appsv1.StatefulSet{}
		Eventually(func() error {
			return k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: "default"}, sts)
		}, "10s", "250ms").Should(Succeed())
		sts.Status.Replicas = replicas
		sts.Status.ReadyReplicas = replicas
		Expect(k8sClient.Status().Update(ctx, sts)).To(Succeed())
	}

	It("RF2 미달 shard 를 자동 복제해 내구성을 수리한다", func() {
		// peer ID 는 스펙별 고유 대역 의무 — 전역 Fake 잔존 컬렉션이 타 스펙 peers 와 겹치면
		// (dr4 실측: drain 이 비우는 peer 를 rebalance 가 rfvec 으로 되채우는 영구 경쟁) 간섭한다.
		fakeQdrant.SetPeers(
			qdrant.Peer{ID: 61, URI: "http://rf1-0.rf1-headless:6335/"},
			qdrant.Peer{ID: 62, URI: "http://rf1-1.rf1-headless:6335/"},
		)
		fakeQdrant.SetCollection("rfvec", qdrant.CollectionInfo{
			Exists: true, VectorSize: 4, Distance: "Cosine", ShardNumber: 2, ReplicationFactor: 2,
		})
		// count 균형(1:1)이지만 각 shard replica 1 < RF 2 — 리밸런스가 아닌 재복제가 발동해야 한다.
		fakeQdrant.SetPlacement("rfvec", map[uint32]uint64{0: 61, 1: 62})

		qc := &qdrantv1alpha1.QdrantCluster{ObjectMeta: metav1.ObjectMeta{Name: "rf1", Namespace: "default"}}
		qc.Spec.Replicas = 2
		Expect(k8sClient.Create(ctx, qc)).To(Succeed())
		makeReady("rf1", 2)

		// 수렴: 두 shard 모두 replica 2 도달 (Replicated 기록 — 본 컬렉션 것만 집계).
		Eventually(func() int {
			n := 0
			for _, m := range fakeQdrant.Replicated {
				if strings.HasPrefix(m, "rfvec/") {
					n++
				}
			}
			return n
		}, "25s", "250ms").Should(Equal(2), "shard 2개 각각 복제 1건씩")
		Expect(fakeQdrant.Replicated).To(ContainElement("rfvec/0:61->62"), "결정론 첫 복제")
		Expect(fakeQdrant.Replicated).To(ContainElement("rfvec/1:62->61"))

		// RF 충족 후 재복제·리밸런스 무행동으로 Running 정착 + lane 정산.
		fetched := &qdrantv1alpha1.QdrantCluster{}
		Eventually(func() string {
			_ = k8sClient.Get(ctx, types.NamespacedName{Name: "rf1", Namespace: "default"}, fetched)
			return fetched.Status.Phase
		}, "15s", "250ms").Should(Equal("Running"))
		Expect(fetched.Status.ActiveMove).To(BeNil())
		Expect(fetched.Status.PlannedMoves).To(BeEmpty())
	})
})

var _ = Describe("QdrantCluster HA 자산 (v0.5.0)", func() {
	It("replicas>=2 면 PDB 를 만들고, 1 로 줄이면 제거한다", func() {
		key := types.NamespacedName{Name: "ha5", Namespace: "default"}
		qc := &qdrantv1alpha1.QdrantCluster{ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace}}
		qc.Spec.Replicas = 2
		Expect(k8sClient.Create(ctx, qc)).To(Succeed())

		pdb := &policyv1.PodDisruptionBudget{}
		Eventually(func() error {
			return k8sClient.Get(ctx, key, pdb)
		}, "15s", "250ms").Should(Succeed(), "replicas=2 면 PDB 생성")
		Expect(pdb.Spec.MinAvailable.IntValue()).To(Equal(1))
		Expect(metav1.GetControllerOf(pdb).Kind).To(Equal("QdrantCluster"), "PDB 는 CR 소유(삭제 시 함께 정리)")

		// STS 에도 soft anti-affinity 가 주입돼야 한다.
		sts := &appsv1.StatefulSet{}
		Expect(k8sClient.Get(ctx, key, sts)).To(Succeed())
		Expect(sts.Spec.Template.Spec.Affinity).NotTo(BeNil())
		Expect(sts.Spec.Template.Spec.Affinity.PodAntiAffinity.PreferredDuringSchedulingIgnoredDuringExecution).To(HaveLen(1))

		// 1 로 축소하면 PDB 제거(단일 파드 PDB = drain 영구 차단 방지).
		fetched := &qdrantv1alpha1.QdrantCluster{}
		Expect(k8sClient.Get(ctx, key, fetched)).To(Succeed())
		fetched.Spec.Replicas = 1
		Expect(k8sClient.Update(ctx, fetched)).To(Succeed())
		Eventually(func() bool {
			return apierrors.IsNotFound(k8sClient.Get(ctx, key, &policyv1.PodDisruptionBudget{}))
		}, "20s", "250ms").Should(BeTrue(), "replicas=1 이면 PDB 제거")
	})
})
