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

package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// 포트 상수 — Qdrant 컨테이너/서비스/빌더 전체가 참조하는 SSOT
const (
	RESTPort int32 = 6333
	GRPCPort int32 = 6334
	P2PPort  int32 = 6335
)

// ImageSpec은 Qdrant 컨테이너 이미지를 정의한다
type ImageSpec struct {
	// +kubebuilder:default="qdrant/qdrant"
	Repository string `json:"repository,omitempty"`
	// +kubebuilder:default="v1.18.2"
	Tag string `json:"tag,omitempty"`
}

// RetentionPolicy는 QdrantCluster 삭제 시 PVC 보존 정책이다
// +kubebuilder:validation:Enum=Retain;Delete
type RetentionPolicy string

const (
	RetentionRetain RetentionPolicy = "Retain"
	RetentionDelete RetentionPolicy = "Delete"
)

// PersistenceSpec은 Qdrant 데이터 볼륨 설정을 정의한다
type PersistenceSpec struct {
	// +kubebuilder:default="10Gi"
	// 포인터여야 함 — non-pointer resource.Quantity(struct)는 omitempty 가 무효라 zero 값도 "0" 으로
	// 직렬화되어 타입 Go 클라이언트 생성 시 CRD default(10Gi) 미적용. nil 포인터여야 default 발동.
	Size *resource.Quantity `json:"size,omitempty"`
	// +kubebuilder:default="ceph-rbd"
	StorageClassName string `json:"storageClassName,omitempty"`
	// +kubebuilder:default={ReadWriteOnce}
	AccessModes []corev1.PersistentVolumeAccessMode `json:"accessModes,omitempty"`
	// +kubebuilder:default="Retain"
	RetentionPolicy RetentionPolicy `json:"retentionPolicy,omitempty"`
}

// ConfigSpec은 Qdrant production.yaml 관련 설정을 정의한다
type ConfigSpec struct {
	// +kubebuilder:default=true
	ClusterEnabled bool `json:"clusterEnabled,omitempty"`
	TLSEnabled     bool `json:"tlsEnabled,omitempty"`
	// production.yaml 전체 passthrough (escape hatch)
	// +kubebuilder:pruning:PreserveUnknownFields
	RawOverride *apiextensionsv1.JSON `json:"rawOverride,omitempty"`
}

// SecretKeyRef는 API 키를 담은 Secret 참조다
type SecretKeyRef struct {
	Name string `json:"name"`
	// +kubebuilder:default="api-key"
	Key string `json:"key,omitempty"`
}

// QdrantClusterSpec defines the desired state of QdrantCluster
type QdrantClusterSpec struct {
	Image ImageSpec `json:"image,omitempty"`
	// +kubebuilder:default=1
	// +kubebuilder:validation:Minimum=1
	Replicas    int32                       `json:"replicas,omitempty"`
	Resources   corev1.ResourceRequirements `json:"resources,omitempty"`
	Persistence PersistenceSpec             `json:"persistence,omitempty"`
	Config      ConfigSpec                  `json:"config,omitempty"`
	// +kubebuilder:default="ClusterIP"
	ServiceType        corev1.ServiceType `json:"serviceType,omitempty"`
	ServiceAnnotations map[string]string  `json:"serviceAnnotations,omitempty"`
	APIKey             *SecretKeyRef      `json:"apiKey,omitempty"`
	// +kubebuilder:default=1000
	RunAsUser int64 `json:"runAsUser,omitempty"`
	// +kubebuilder:default=3000
	FSGroup      int64               `json:"fsGroup,omitempty"`
	NodeSelector map[string]string   `json:"nodeSelector,omitempty"`
	Tolerations  []corev1.Toleration `json:"tolerations,omitempty"`
	Affinity     *corev1.Affinity    `json:"affinity,omitempty"`
}

// QdrantClusterStatus defines the observed state of QdrantCluster.
type QdrantClusterStatus struct {
	Phase              string   `json:"phase,omitempty"`
	Replicas           int32    `json:"replicas,omitempty"`
	ReadyReplicas      int32    `json:"readyReplicas,omitempty"`
	Peers              []string `json:"peers,omitempty"`
	ObservedGeneration int64    `json:"observedGeneration,omitempty"`

	// conditions represent the current state of the QdrantCluster resource.
	// Each condition has a unique type and reflects the status of a specific aspect of the resource.
	//
	// Standard condition types include:
	// - "Available": the resource is fully functional
	// - "Progressing": the resource is being created or updated
	// - "Degraded": the resource failed to reach or maintain its desired state
	//
	// The status of each condition is one of True, False, or Unknown.
	// +patchMergeKey=type
	// +patchStrategy=merge
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

// QdrantCluster is the Schema for the qdrantclusters API
type QdrantCluster struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of QdrantCluster
	// +required
	Spec QdrantClusterSpec `json:"spec"`

	// status defines the observed state of QdrantCluster
	// +optional
	Status QdrantClusterStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// QdrantClusterList contains a list of QdrantCluster
type QdrantClusterList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []QdrantCluster `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &QdrantCluster{}, &QdrantClusterList{})
		return nil
	})
}
