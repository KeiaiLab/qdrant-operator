/*
Copyright 2026 Keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package controller

import (
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	qdrantv1alpha1 "github.com/keiailab/qdrant-operator/api/v1alpha1"
	"github.com/keiailab/qdrant-operator/internal/qdrant"
)

var _ = Describe("QdrantRestore 스냅샷 복원 (v0.10.0)", func() {
	makeReady := func(name string, replicas int32) {
		sts := &appsv1.StatefulSet{}
		Eventually(func() error {
			return k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: "default"}, sts)
		}, "10s", "250ms").Should(Succeed())
		sts.Status.Replicas = replicas
		sts.Status.ReadyReplicas = replicas
		Expect(k8sClient.Status().Update(ctx, sts)).To(Succeed())
	}

	degradedReason := func(key types.NamespacedName) string {
		rs := &qdrantv1alpha1.QdrantRestore{}
		_ = k8sClient.Get(ctx, key, rs)
		for _, c := range rs.Status.Conditions {
			if c.Type == condDegraded && c.Status == metav1.ConditionTrue {
				return c.Reason
			}
		}
		return ""
	}

	It("백업 세대를 peer 마다 되돌린다", func() {
		fakeQdrant.SetPeers(
			qdrant.Peer{ID: 91, URI: "http://rs1-0.rs1-headless:6335/"},
			qdrant.Peer{ID: 92, URI: "http://rs1-1.rs1-headless:6335/"},
		)
		for _, ordinal := range []int32{0, 1} {
			peerFake(ordinal).SetCollection("rsvec", qdrant.CollectionInfo{Exists: true})
		}

		cluster := &qdrantv1alpha1.QdrantCluster{ObjectMeta: metav1.ObjectMeta{Name: "rs1", Namespace: "default"}}
		cluster.Spec.Replicas = 2
		Expect(k8sClient.Create(ctx, cluster)).To(Succeed())
		makeReady("rs1", 2)

		// 먼저 백업 한 세대를 만든다 — 복원은 그 좌표를 읽는다.
		bk := &qdrantv1alpha1.QdrantBackup{ObjectMeta: metav1.ObjectMeta{Name: "rs1-backup", Namespace: "default"}}
		bk.Spec.ClusterRef = "rs1"
		bk.Spec.Collections = []string{"rsvec"}
		Expect(k8sClient.Create(ctx, bk)).To(Succeed())

		bkKey := types.NamespacedName{Name: "rs1-backup", Namespace: "default"}
		fetchedBk := &qdrantv1alpha1.QdrantBackup{}
		Eventually(func() int {
			_ = k8sClient.Get(ctx, bkKey, fetchedBk)
			return len(fetchedBk.Status.Snapshots)
		}, "30s", "250ms").Should(Equal(2))

		rs := &qdrantv1alpha1.QdrantRestore{ObjectMeta: metav1.ObjectMeta{Name: "rs1-restore", Namespace: "default"}}
		rs.Spec.ClusterRef = "rs1"
		rs.Spec.Collection = "rsvec"
		rs.Spec.FromBackup = "rs1-backup"
		Expect(k8sClient.Create(ctx, rs)).To(Succeed())

		rsKey := types.NamespacedName{Name: "rs1-restore", Namespace: "default"}
		fetched := &qdrantv1alpha1.QdrantRestore{}
		Eventually(func() string {
			_ = k8sClient.Get(ctx, rsKey, fetched)
			return fetched.Status.Phase
		}, "30s", "250ms").Should(Equal(phaseRestoreDone))
		Expect(fetched.Status.Restored).To(HaveLen(2), "peer 마다 한 번씩 복원해야 한다")
		Expect(fetched.Status.Pending).To(BeEmpty())

		// 각 peer 는 **자기** 스냅샷을 읽는다 — 남의 peer 주소를 주면 안 된다.
		for _, ordinal := range []int32{0, 1} {
			rec := peerFake(ordinal).Recovered
			Expect(rec).NotTo(BeEmpty(), "peer%d 복원 미발행", ordinal)
			last := rec[len(rec)-1]
			Expect(last).To(ContainSubstring("(snapshot)"), "복원 기본 priority 는 snapshot 이어야 한다")
			Expect(last).To(ContainSubstring(fmt.Sprintf("rs1-%d.rs1-headless", ordinal)))
		}

		// 완료된 복원은 다시 돌지 않는다 — 재복원은 그 뒤 데이터를 조용히 되돌린다.
		before := len(peerFake(0).Recovered)
		Consistently(func() int { return len(peerFake(0).Recovered) }, "3s", "500ms").Should(Equal(before))
	})

	It("S3 보관이면 주소를 유추하지 않고 멈춘다", func() {
		cluster := &qdrantv1alpha1.QdrantCluster{ObjectMeta: metav1.ObjectMeta{Name: "rs2", Namespace: "default"}}
		cluster.Spec.Replicas = 1
		cluster.Spec.Snapshots = &qdrantv1alpha1.SnapshotsSpec{
			Storage: qdrantv1alpha1.SnapshotStorageS3,
			S3: &qdrantv1alpha1.S3StorageSpec{
				Bucket: "b", EndpointURL: "http://rgw.svc:80",
				Credentials: qdrantv1alpha1.S3CredentialsRef{Name: "creds"},
			},
		}
		Expect(k8sClient.Create(ctx, cluster)).To(Succeed())

		rs := &qdrantv1alpha1.QdrantRestore{ObjectMeta: metav1.ObjectMeta{Name: "rs2-restore", Namespace: "default"}}
		rs.Spec.ClusterRef = "rs2"
		rs.Spec.Collection = "anything"
		rs.Spec.FromBackup = "없는백업"
		Expect(k8sClient.Create(ctx, rs)).To(Succeed())

		key := types.NamespacedName{Name: "rs2-restore", Namespace: "default"}
		Eventually(func() string { return degradedReason(key) }, "20s", "250ms").Should(Equal(reasonNoSnapshotRefs))

		fetched := &qdrantv1alpha1.QdrantRestore{}
		Expect(k8sClient.Get(ctx, key, fetched)).To(Succeed())
		Expect(fetched.Status.Restored).To(BeEmpty(), "주소를 모르는데 복원을 발행했다")
	})

	It("좌표가 하나도 없으면 발행하지 않는다", func() {
		cluster := &qdrantv1alpha1.QdrantCluster{ObjectMeta: metav1.ObjectMeta{Name: "rs3", Namespace: "default"}}
		cluster.Spec.Replicas = 1
		Expect(k8sClient.Create(ctx, cluster)).To(Succeed())

		rs := &qdrantv1alpha1.QdrantRestore{ObjectMeta: metav1.ObjectMeta{Name: "rs3-restore", Namespace: "default"}}
		rs.Spec.ClusterRef = "rs3"
		rs.Spec.Collection = "nope"
		Expect(k8sClient.Create(ctx, rs)).To(Succeed())

		key := types.NamespacedName{Name: "rs3-restore", Namespace: "default"}
		Eventually(func() string { return degradedReason(key) }, "20s", "250ms").Should(Equal(reasonNoSnapshotRefs))
	})
})
