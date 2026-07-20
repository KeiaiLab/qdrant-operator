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
			ShardNumber:       shard,
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
