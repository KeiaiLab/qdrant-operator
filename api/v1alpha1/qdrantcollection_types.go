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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// VectorsSpec 은 컬렉션 벡터 스키마(단일 벡터). 신규 생성 시 사용하고, 기존 컬렉션
// 채택 시에는 검증 기준으로만 쓴다.
type VectorsSpec struct {
	// +kubebuilder:validation:Minimum=1
	Size uint64 `json:"size"`
	// +kubebuilder:default="Cosine"
	// +kubebuilder:validation:Enum=Cosine;Dot;Euclid;Manhattan
	Distance string `json:"distance,omitempty"`
}

// +kubebuilder:validation:Enum=Retain;Delete
type CollectionDeletePolicy string

const (
	CollectionRetain CollectionDeletePolicy = "Retain"
	CollectionDelete CollectionDeletePolicy = "Delete"
)

// QdrantCollectionSpec — 같은 네임스페이스의 QdrantCluster 위 컬렉션을 선언적으로
// 보장(ensure)·채택(adopt)한다. 파라미터 불일치 시 파괴적 재생성 대신 Degraded 로
// 표면화한다(자동 재생성 절대 금지 — 설계 안전 원칙).
type QdrantCollectionSpec struct {
	// 같은 네임스페이스의 QdrantCluster 이름.
	// +kubebuilder:validation:MinLength=1
	ClusterRef string `json:"clusterRef"`
	// Qdrant 상 컬렉션 이름. 비우면 metadata.name 사용.
	CollectionName string      `json:"collectionName,omitempty"`
	Vectors        VectorsSpec `json:"vectors"`
	// 생성 시 고정 — 이후 변경은 re-shard 워크플로(후속 마일스톤) 대상.
	// +kubebuilder:default=1
	// +kubebuilder:validation:Minimum=1
	ShardNumber uint32 `json:"shardNumber,omitempty"`
	// +kubebuilder:default=1
	// +kubebuilder:validation:Minimum=1
	ReplicationFactor uint32 `json:"replicationFactor,omitempty"`
	// CR 삭제 시 컬렉션 처분 — 기본 Retain(데이터 보존). Delete 명시 시에만 파이널라이저로
	// 컬렉션을 삭제한다.
	// +kubebuilder:default="Retain"
	OnDelete CollectionDeletePolicy `json:"onDelete,omitempty"`
}

// QdrantCollectionStatus — 관측 상태.
type QdrantCollectionStatus struct {
	// Pending | Ready | Degraded
	Phase string `json:"phase,omitempty"`
	// 채택(기존 컬렉션 인수) 여부 — true 면 이 CR 생성 이전부터 존재하던 컬렉션.
	Adopted            bool   `json:"adopted,omitempty"`
	PointsCount        uint64 `json:"pointsCount,omitempty"`
	ObservedGeneration int64  `json:"observedGeneration,omitempty"`
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Cluster",type=string,JSONPath=`.spec.clusterRef`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Points",type=integer,JSONPath=`.status.pointsCount`

// QdrantCollection is the Schema for the qdrantcollections API
type QdrantCollection struct {
	metav1.TypeMeta `json:",inline"`

	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// +required
	Spec QdrantCollectionSpec `json:"spec"`

	// +optional
	Status QdrantCollectionStatus `json:"status,omitzero"`
}

// TargetCollectionName 은 Qdrant 상 실제 컬렉션 이름(미지정 시 CR 이름).
func (qc *QdrantCollection) TargetCollectionName() string {
	if qc.Spec.CollectionName != "" {
		return qc.Spec.CollectionName
	}
	return qc.Name
}

// +kubebuilder:object:root=true

// QdrantCollectionList contains a list of QdrantCollection
type QdrantCollectionList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []QdrantCollection `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &QdrantCollection{}, &QdrantCollectionList{})
		return nil
	})
}
