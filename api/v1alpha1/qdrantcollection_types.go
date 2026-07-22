/*
Copyright 2026 Keiailab.

Licensed under the MIT License. See the LICENSE file for details.
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
	// 포인터화(R3-3) — nil 이면 "라이브 값 채택"(리샤드 트리거 아님): 생성 시 1, 채택 시
	// 라이브 shardNumber 그대로. 명시 설정 시에만 목표가 되며, 라이브와 단독 상이하면
	// reshard 정책(Auto+alias 필요)에 따라 리샤드 워크플로 대상이 된다.
	// +kubebuilder:validation:Minimum=1
	// +optional
	ShardNumber *uint32 `json:"shardNumber,omitempty"`
	// +kubebuilder:default=1
	// +kubebuilder:validation:Minimum=1
	ReplicationFactor uint32 `json:"replicationFactor,omitempty"`
	// CR 삭제 시 컬렉션 처분 — 기본 Retain(데이터 보존). Delete 명시 시에만 파이널라이저로
	// 컬렉션을 삭제한다.
	// +kubebuilder:default="Retain"
	OnDelete CollectionDeletePolicy `json:"onDelete,omitempty"`

	// Alias 는 소비자가 접근하는 논리 이름 — 설정 시 컨트롤러가 항상 라이브 물리 컬렉션에
	// 정렬한다. 무중단 리샤드 스왑의 전제(alias 없이는 shardNumber 변경이 Degraded 로만 표면화).
	// +optional
	Alias string `json:"alias,omitempty"`
	// Reshard 는 shardNumber 단독 상이 시 처리 정책 — 파괴적/고비용 동작의 명시 opt-in
	// (onDelete=Retain 과 같은 안전 기본값 패턴).
	// +kubebuilder:validation:Enum=Manual;Auto
	// +kubebuilder:default="Manual"
	Reshard ReshardPolicy `json:"reshard,omitempty"`
}

// +kubebuilder:validation:Enum=Manual;Auto
type ReshardPolicy string

const (
	ReshardManual ReshardPolicy = "Manual"
	ReshardAuto   ReshardPolicy = "Auto"
)

// ReshardStatus — 리샤드 워크플로 진행(실행 전 계획 선노출 포함).
type ReshardStatus struct {
	// Preparing|Copying|Swapping|Finalizing|Failed|Blocked
	Phase            string `json:"phase"`
	SourceCollection string `json:"sourceCollection"`
	// <physical>-rs-g<generation> — 시작 시 확정·고정(SSOT).
	ShadowCollection  string `json:"shadowCollection"`
	TargetShardNumber uint32 `json:"targetShardNumber"`
	CopiedPoints      uint64 `json:"copiedPoints,omitempty"`
	TotalPoints       uint64 `json:"totalPoints,omitempty"`
	// scroll next_page_offset(JSON 인코딩) — 재개 커서.
	// +optional
	Cursor    string       `json:"cursor,omitempty"`
	StartedAt *metav1.Time `json:"startedAt,omitempty"`
	Attempts  int32        `json:"attempts,omitempty"`
}

// QdrantCollectionStatus — 관측 상태.
type QdrantCollectionStatus struct {
	// Pending | Ready | Degraded
	Phase string `json:"phase,omitempty"`
	// 채택(기존 컬렉션 인수) 여부 — true 면 이 CR 생성 이전부터 존재하던 컬렉션.
	Adopted            bool   `json:"adopted,omitempty"`
	PointsCount        uint64 `json:"pointsCount,omitempty"`
	ObservedGeneration int64  `json:"observedGeneration,omitempty"`
	// ActiveCollection 은 이 CR 을 현재 뒷받침하는 물리 컬렉션(alias 타깃) — 리샤드 성공
	// 스왑마다 갱신. 빈 값 = TargetCollectionName() 과 동일.
	// +optional
	ActiveCollection string `json:"activeCollection,omitempty"`
	// 리샤드 워크플로 진행 — nil 이면 리샤드 없음.
	// +optional
	Reshard *ReshardStatus `json:"reshard,omitempty"`
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
