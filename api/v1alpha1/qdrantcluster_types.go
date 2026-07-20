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
	FSGroup int64 `json:"fsGroup,omitempty"`
	// B-3 shard 재배치 제어 — 미지정 시 활성(enabled=true). enabled=false 는 dry-run
	// (계획 status 노출만, 이동 미발행).
	// +optional
	Rebalance *RebalanceSpec `json:"rebalance,omitempty"`

	NodeSelector map[string]string   `json:"nodeSelector,omitempty"`
	Tolerations  []corev1.Toleration `json:"tolerations,omitempty"`
	Affinity     *corev1.Affinity    `json:"affinity,omitempty"`
}

// QdrantClusterStatus defines the observed state of QdrantCluster.
// RebalanceSpec 은 B-3 shard 재배치 동작 제어.
type RebalanceSpec struct {
	// false 면 dry-run — 관측·계획(plannedMoves 노출)만 하고 이동을 발행하지 않는다.
	// 포인터인 이유: bool 은 false 를 "미지정"과 구별할 수 없다(omitempty 함정).
	// +kubebuilder:default=true
	Enabled *bool `json:"enabled,omitempty"`
}

// RebalanceMoveStatus 는 발행된(진행 중일 수 있는) 이동 1건의 추적 상태.
// peer id 는 uint64 전 범위라 문자열(십진)로 보고한다.
type RebalanceMoveStatus struct {
	Collection string       `json:"collection"`
	ShardID    int32        `json:"shardId"`
	FromPeer   string       `json:"fromPeer"`
	ToPeer     string       `json:"toPeer"`
	IssuedAt   *metav1.Time `json:"issuedAt,omitempty"`
	// 연속 실패 횟수 — 백오프 계산에 사용, 완료 관측 시 0 으로 리셋.
	FailureCount int32 `json:"failureCount,omitempty"`
}

// PeerShards 는 한 peer 가 보유한 shard 수. Peer 는 qdrant peer id 의 십진 문자열 —
// 실측상 peer id 가 int32 를 초과하는 큰 수라 CRD 스키마 호환을 위해 문자열로 보고한다.
type PeerShards struct {
	Peer   string `json:"peer"`
	Shards int32  `json:"shards"`
}

// CollectionDistribution 은 컬렉션 1개의 peer 별 shard 분포 관측(B-2).
type CollectionDistribution struct {
	Collection string `json:"collection"`
	// +optional
	PerPeer []PeerShards `json:"perPeer,omitempty"`
	// 진행 중 shard 전송 수 — 0 이 아닌 동안 rebalance/drain 은 새 이동을 발행하지 않는다.
	// +optional
	TransfersInFlight int32 `json:"transfersInFlight,omitempty"`
}

type QdrantClusterStatus struct {
	Phase              string   `json:"phase,omitempty"`
	Replicas           int32    `json:"replicas,omitempty"`
	ReadyReplicas      int32    `json:"readyReplicas,omitempty"`
	Peers              []string `json:"peers,omitempty"`
	ObservedGeneration int64    `json:"observedGeneration,omitempty"`

	// B-2 관측: 컬렉션별 peer 간 shard 분포. Running 상태에서 주기 갱신되며, steady-state
	// 에서 오퍼레이터는 이 관측(GET) 외 어떤 행동도 하지 않는다.
	// +optional
	ShardDistribution []CollectionDistribution `json:"shardDistribution,omitempty"`
	// B-3 계획 선노출: 실행 예정 이동("collection/shard: from->to"). 실행 전 항상 여기 먼저
	// 나타난다(관측 가능성 원칙) — 비어 있으면 이동 없음.
	// +optional
	PlannedMoves []string `json:"plannedMoves,omitempty"`
	// B-3 발행 이동 추적 — nil 이면 발행 중인 이동 없음.
	// +optional
	Rebalance *RebalanceMoveStatus `json:"rebalance,omitempty"`

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
