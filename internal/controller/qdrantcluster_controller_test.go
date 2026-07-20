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
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	qdrantv1alpha1 "github.com/keiailab/qdrant-operator/api/v1alpha1"
)

var _ = Describe("QdrantCluster Controller", func() {
	Context("When reconciling a resource", func() {
		const (
			resourceName      = "test-resource"
			resourceNamespace = "default"
		)

		ctx := context.Background()

		typeNamespacedName := types.NamespacedName{
			Name:      resourceName,
			Namespace: resourceNamespace,
		}
		qdrantcluster := &qdrantv1alpha1.QdrantCluster{}

		BeforeEach(func() {
			By("creating the custom resource for the Kind QdrantCluster")
			err := k8sClient.Get(ctx, typeNamespacedName, qdrantcluster)
			if err != nil && errors.IsNotFound(err) {
				resource := &qdrantv1alpha1.QdrantCluster{
					ObjectMeta: metav1.ObjectMeta{
						Name:      resourceName,
						Namespace: resourceNamespace,
					},
					// TODO(user): Specify other spec details if needed.
				}
				Expect(k8sClient.Create(ctx, resource)).To(Succeed())
			}
		})

		AfterEach(func() {
			// TODO(user): Cleanup logic after each test, like removing the resource instance.
			resource := &qdrantv1alpha1.QdrantCluster{}
			err := k8sClient.Get(ctx, typeNamespacedName, resource)
			Expect(err).NotTo(HaveOccurred())

			By("Cleanup the specific resource instance QdrantCluster")
			Expect(k8sClient.Delete(ctx, resource)).To(Succeed())
		})
		It("should successfully reconcile the resource", func() {
			By("Reconciling the created resource")
			controllerReconciler := &QdrantClusterReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())
			// TODO(user): Add more specific assertions depending on your controller's reconciliation logic.
			// Example: If you expect a certain status condition after reconciliation, verify it here.
		})
	})

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
})
