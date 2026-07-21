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
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	qdrantv1alpha1 "github.com/keiailab/qdrant-operator/api/v1alpha1"
	"github.com/keiailab/qdrant-operator/internal/qdrant"
)

// fakeQdrant 는 suite_test.go BeforeSuite 에서 초기화되어 QdrantCollection 컨트롤러에
// 주입된다 — 시나리오들이 상태 세팅/검증에 직접 사용한다.
var fakeQdrant *qdrant.Fake

// collectionTestCluster — 본 파일 시나리오 공용 QdrantCluster 이름.
const collectionTestCluster = "colc1"

// 시나리오별 고유 이름 — 공유 default ns 에서 이전 spec 잔재와의 순서 결합을 피한다
// (envtest 는 GC 를 돌리지 않는다). suite 전역 ctx 를 사용한다.
func newCollectionCR(name string, shard uint32, onDelete qdrantv1alpha1.CollectionDeletePolicy) *qdrantv1alpha1.QdrantCollection {
	return &qdrantv1alpha1.QdrantCollection{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec: qdrantv1alpha1.QdrantCollectionSpec{
			ClusterRef:        collectionTestCluster,
			Vectors:           qdrantv1alpha1.VectorsSpec{Size: 384, Distance: "Cosine"},
			ShardNumber:       &shard,
			ReplicationFactor: 1,
			OnDelete:          onDelete,
		},
	}
}

var _ = Describe("QdrantCollection Controller", func() {
	It("클러스터가 있으면 새 컬렉션을 생성하고 Ready 가 된다", func() {
		Expect(k8sClient.Create(ctx, &qdrantv1alpha1.QdrantCluster{
			ObjectMeta: metav1.ObjectMeta{Name: collectionTestCluster, Namespace: "default"},
		})).To(Succeed())
		Expect(k8sClient.Create(ctx, newCollectionCR("vec1", 2, ""))).To(Succeed())

		fetched := &qdrantv1alpha1.QdrantCollection{}
		Eventually(func() string {
			_ = k8sClient.Get(ctx, types.NamespacedName{Name: "vec1", Namespace: "default"}, fetched)
			return fetched.Status.Phase
		}, "10s", "250ms").Should(Equal("Ready"))

		info, err := fakeQdrant.GetCollection(ctx, "vec1")
		Expect(err).NotTo(HaveOccurred())
		Expect(info.Exists).To(BeTrue())
		Expect(info.ShardNumber).To(Equal(uint32(2)))
		Expect(fetched.Status.Adopted).To(BeFalse())
	})

	It("파라미터가 일치하는 기존 컬렉션은 재생성 없이 채택한다", func() {
		fakeQdrant.SetCollection("legacy1", qdrant.CollectionInfo{
			Exists: true, PointsCount: 928287,
			VectorSize: 384, Distance: "Cosine", ShardNumber: 1, ReplicationFactor: 1,
		})
		fakeQdrant.SetPlacement("legacy1", map[uint32]uint64{0: 1})
		Expect(k8sClient.Create(ctx, newCollectionCR("legacy1", 1, ""))).To(Succeed())

		fetched := &qdrantv1alpha1.QdrantCollection{}
		Eventually(func() bool {
			_ = k8sClient.Get(ctx, types.NamespacedName{Name: "legacy1", Namespace: "default"}, fetched)
			return fetched.Status.Phase == "Ready" && fetched.Status.Adopted
		}, "10s", "250ms").Should(BeTrue(), "채택(adopted) Ready 여야 함")
		Expect(fetched.Status.PointsCount).To(Equal(uint64(928287)))
		// 채택이지 재생성이 아니다 — Created 기록에 없어야 한다.
		Expect(fakeQdrant.Created).NotTo(ContainElement("legacy1"))
	})

	It("파라미터 불일치는 Degraded 로 표면화하고 절대 재생성하지 않는다", func() {
		fakeQdrant.SetCollection("mismatch1", qdrant.CollectionInfo{
			Exists: true, VectorSize: 384, Distance: "Cosine", ShardNumber: 4, ReplicationFactor: 1,
		})
		Expect(k8sClient.Create(ctx, newCollectionCR("mismatch1", 1, ""))).To(Succeed())

		fetched := &qdrantv1alpha1.QdrantCollection{}
		Eventually(func() string {
			_ = k8sClient.Get(ctx, types.NamespacedName{Name: "mismatch1", Namespace: "default"}, fetched)
			return fetched.Status.Phase
		}, "10s", "250ms").Should(Equal("Degraded"))
		// 원본 무손상 + 재생성/삭제 시도 0.
		mi, _ := fakeQdrant.GetCollection(ctx, "mismatch1")
		Expect(mi.ShardNumber).To(Equal(uint32(4)))
		Expect(fakeQdrant.Created).NotTo(ContainElement("mismatch1"))
		Expect(fakeQdrant.Deleted).NotTo(ContainElement("mismatch1"))
	})

	It("onDelete=Delete 만 CR 삭제 시 컬렉션을 지우고, Retain 은 보존한다", func() {
		// Delete 정책
		Expect(k8sClient.Create(ctx, newCollectionCR("deleteme1", 1, qdrantv1alpha1.CollectionDelete))).To(Succeed())
		delCR := &qdrantv1alpha1.QdrantCollection{}
		Eventually(func() string {
			_ = k8sClient.Get(ctx, types.NamespacedName{Name: "deleteme1", Namespace: "default"}, delCR)
			return delCR.Status.Phase
		}, "10s", "250ms").Should(Equal("Ready"))
		Expect(k8sClient.Delete(ctx, delCR)).To(Succeed())
		Eventually(func() []string { return fakeQdrant.Deleted }, "10s", "250ms").Should(ContainElement("deleteme1"))

		// Retain(기본) 정책
		Expect(k8sClient.Create(ctx, newCollectionCR("keepme1", 1, ""))).To(Succeed())
		keepCR := &qdrantv1alpha1.QdrantCollection{}
		Eventually(func() string {
			_ = k8sClient.Get(ctx, types.NamespacedName{Name: "keepme1", Namespace: "default"}, keepCR)
			return keepCR.Status.Phase
		}, "10s", "250ms").Should(Equal("Ready"))
		Expect(k8sClient.Delete(ctx, keepCR)).To(Succeed())
		Eventually(func() bool {
			err := k8sClient.Get(ctx, types.NamespacedName{Name: "keepme1", Namespace: "default"}, &qdrantv1alpha1.QdrantCollection{})
			return err != nil
		}, "10s", "250ms").Should(BeTrue(), "Retain CR 은 파이널라이저 없이 즉시 사라져야 함")
		info, err := fakeQdrant.GetCollection(ctx, "keepme1")
		Expect(err).NotTo(HaveOccurred())
		Expect(info.Exists).To(BeTrue(), "Retain — 데이터 보존")
		Expect(fakeQdrant.Deleted).NotTo(ContainElement("keepme1"))
	})
})

// B-5 re-shard — shadow 복사 + alias 원자 스왑. 커밋점(스왑) 전 실패는 원본 무손상.
// 순서 결합 금지(랜덤 컨테이너 순서): 자체 클러스터(rc5)를 각 spec 이 ensure 한다.
var _ = Describe("QdrantCollection re-shard (B-5)", func() {
	ensureCluster := func() {
		err := k8sClient.Create(ctx, &qdrantv1alpha1.QdrantCluster{
			ObjectMeta: metav1.ObjectMeta{Name: "rc5", Namespace: "default"},
		})
		if err != nil && !apierrors.IsAlreadyExists(err) {
			Expect(err).NotTo(HaveOccurred())
		}
	}
	It("3중 opt-in(명시 shardNumber+Auto+alias) 충족 시 무중단 리샤드를 완주한다", func() {
		fakeQdrant.SetCollection("oldvec", qdrant.CollectionInfo{
			Exists: true, VectorSize: 384, Distance: "Cosine", ShardNumber: 1, ReplicationFactor: 1,
		})
		fakeQdrant.SetPlacement("oldvec", map[uint32]uint64{0: 1})
		fakeQdrant.SetPoints("oldvec", 300) // 배치 128 → 3 pass 복사

		ensureCluster()
		two := uint32(2)
		cr := newCollectionCR("rsv5", 1, "")
		cr.Spec.ClusterRef = "rc5"
		cr.Spec.CollectionName = "oldvec"
		cr.Spec.ShardNumber = &two
		cr.Spec.Alias = "vecalias"
		cr.Spec.Reshard = qdrantv1alpha1.ReshardAuto
		Expect(k8sClient.Create(ctx, cr)).To(Succeed())

		fetched := &qdrantv1alpha1.QdrantCollection{}
		Eventually(func() string {
			_ = k8sClient.Get(ctx, types.NamespacedName{Name: "rsv5", Namespace: "default"}, fetched)
			return fetched.Status.Phase
		}, "30s", "250ms").Should(Equal("Ready"), "리샤드 완주 후 Ready")

		shadow := "oldvec-rs-g1"
		Expect(fetched.Status.ActiveCollection).To(Equal(shadow), "진본이 shadow 로 전환")
		Expect(fetched.Status.Reshard).To(BeNil(), "워크플로 정리")
		aliases, _ := fakeQdrant.ListAliases(ctx)
		Expect(aliases["vecalias"]).To(Equal(shadow), "alias 원자 스왑")
		si, _ := fakeQdrant.GetCollection(ctx, shadow)
		Expect(si.PointsCount).To(Equal(uint64(300)), "전량 복사")
		Expect(si.ShardNumber).To(Equal(uint32(2)), "새 shard 수")
		oi, _ := fakeQdrant.GetCollection(ctx, "oldvec")
		Expect(oi.Exists).To(BeTrue(), "onDelete=Retain — 원본 보존")
	})

	It("게이트 미충족(shardNumber 상이 + alias 없음)은 ReshardRequired 로만 표면화한다", func() {
		fakeQdrant.SetCollection("gated1", qdrant.CollectionInfo{
			Exists: true, VectorSize: 384, Distance: "Cosine", ShardNumber: 3, ReplicationFactor: 1,
		})
		ensureCluster()
		g1 := newCollectionCR("gated1", 1, "")
		g1.Spec.ClusterRef = "rc5"
		Expect(k8sClient.Create(ctx, g1)).To(Succeed()) // 명시 shard 1 ≠ live 3, alias 없음

		fetched := &qdrantv1alpha1.QdrantCollection{}
		Eventually(func() string {
			_ = k8sClient.Get(ctx, types.NamespacedName{Name: "gated1", Namespace: "default"}, fetched)
			c := meta.FindStatusCondition(fetched.Status.Conditions, "Degraded")
			if c == nil {
				return ""
			}
			return c.Reason
		}, "10s", "250ms").Should(Equal("ReshardRequired"))
		Expect(fakeQdrant.Created).NotTo(ContainElement(ContainSubstring("gated1-rs-")), "shadow 미생성")
	})

	It("size 상이는 ReshardRequired 가 아니라 ParamsMismatch 다 (상호배타)", func() {
		fakeQdrant.SetCollection("sized1", qdrant.CollectionInfo{
			Exists: true, VectorSize: 768, Distance: "Cosine", ShardNumber: 1, ReplicationFactor: 1,
		})
		ensureCluster()
		s1 := newCollectionCR("sized1", 1, "")
		s1.Spec.ClusterRef = "rc5"
		Expect(k8sClient.Create(ctx, s1)).To(Succeed()) // spec size 384 ≠ 768

		fetched := &qdrantv1alpha1.QdrantCollection{}
		Eventually(func() string {
			_ = k8sClient.Get(ctx, types.NamespacedName{Name: "sized1", Namespace: "default"}, fetched)
			c := meta.FindStatusCondition(fetched.Status.Conditions, "Degraded")
			if c == nil {
				return ""
			}
			return c.Reason
		}, "10s", "250ms").Should(Equal("ParamsMismatch"))
	})
})

var _ = Describe("QdrantCollection RF 승격 채택 (v0.5.0)", func() {
	It("라이브 RF 가 spec 보다 작으면 Degraded 가 아니라 채택 + 목표 상향이다", func() {
		// qdrant 는 기존 컬렉션 RF 변경 API 가 없다(PATCH no-op 실측) — CR 이 더 큰 RF 를
		// 요구하면 채택하고, 실제 수렴은 클러스터 컨트롤러의 재복제가 맡는다.
		fakeQdrant.SetCollection("rfadopt", qdrant.CollectionInfo{
			Exists: true, VectorSize: 8, Distance: "Cosine", ShardNumber: 2, ReplicationFactor: 1,
		})
		fakeQdrant.SetPlacement("rfadopt", map[uint32]uint64{0: 1, 1: 1})

		col := &qdrantv1alpha1.QdrantCollection{ObjectMeta: metav1.ObjectMeta{Name: "rfadopt", Namespace: "default"}}
		col.Spec.ClusterRef = "rfc5"
		col.Spec.Vectors = qdrantv1alpha1.VectorsSpec{Size: 8, Distance: "Cosine"}
		col.Spec.ReplicationFactor = 2 // 라이브(1) 보다 큼 = 승격 요구
		two := uint32(2)
		col.Spec.ShardNumber = &two
		// 자체 클러스터 ensure(랜덤 컨테이너 순서 결합 금지 — B-5 스펙과 동일 패턴).
		if err := k8sClient.Create(ctx, &qdrantv1alpha1.QdrantCluster{
			ObjectMeta: metav1.ObjectMeta{Name: "rfc5", Namespace: "default"},
		}); err != nil && !apierrors.IsAlreadyExists(err) {
			Expect(err).NotTo(HaveOccurred())
		}
		Expect(k8sClient.Create(ctx, col)).To(Succeed())

		fetched := &qdrantv1alpha1.QdrantCollection{}
		Eventually(func() string {
			_ = k8sClient.Get(ctx, types.NamespacedName{Name: "rfadopt", Namespace: "default"}, fetched)
			return fetched.Status.Phase
		}, "20s", "250ms").Should(Equal("Ready"), "RF 승격 요구는 ParamsMismatch 가 아니라 채택")
		Expect(fetched.Status.Adopted).To(BeTrue())
		Expect(fakeQdrant.Created).NotTo(ContainElement("rfadopt"), "채택이므로 재생성 금지")

		// 전역 Fake 잔재 정리 — RF2 컬렉션을 남기면 다른 스펙(drain/rebalance)의 관측에
		// 섞여 "RF 미달" 재복제 계획이 계속 발행되고 phase 가 Running 에 도달하지 못한다
		// (dr4 실측 간섭). RF1 잔재는 무해하지만 RF>=2 는 반드시 치운다.
		Expect(k8sClient.Delete(ctx, fetched)).To(Succeed())
		Expect(fakeQdrant.DeleteCollection(ctx, "rfadopt")).To(Succeed())
	})
})
