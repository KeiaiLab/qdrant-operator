/*
Copyright 2026 Keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// QdrantRestore 는 스냅샷에서 컬렉션을 되돌리는 1회성 선언이다.
//
// 복원은 노드 단위다 — qdrant 문서의 표현대로 "호스트마다" 스냅샷 위치를 지정해야 한다.
// 그래서 이 CR 하나가 peer 별 복원 여럿을 묶는다.
//
// 이 컨트롤러는 **아무것도 지우지 않는다.** 깨끗한 복원을 위해 컬렉션을 지웠다 다시 만드는
// 절차가 qdrant 문서에 있지만, 그 삭제는 되돌릴 수 없다. 대신 priority=snapshot 이 기본이라
// 충돌은 스냅샷 쪽이 이긴다 — "되돌린다"는 뜻은 지키면서 파괴 동작은 하지 않는다.
type QdrantRestoreSpec struct {
	// ClusterRef 는 같은 네임스페이스의 QdrantCluster 이름이다.
	ClusterRef string `json:"clusterRef"`

	// Collection 은 복원 대상 컬렉션이다. 없으면 복원이 만들어 준다.
	Collection string `json:"collection"`

	// FromBackup 은 QdrantBackup 이름이다. 그 CR 의 status.snapshots 가 좌표가 된다 —
	// 세대를 통째로 되돌리는 정상 경로다.
	//
	// 스냅샷이 S3 에 있으면 이 경로를 쓸 수 없다. peer 가 자기 스냅샷을 HTTP 로 서빙하지
	// 않고 qdrant 는 s3:// 위치를 받지 않기 때문이다 — 그때는 Sources 로 주소를 준다.
	// +optional
	FromBackup string `json:"fromBackup,omitempty"`

	// Sources 는 peer 별 스냅샷 위치를 직접 주는 경로다. FromBackup 과 함께 쓰면 이쪽이 이긴다.
	// +optional
	Sources []RestoreSource `json:"sources,omitempty"`

	// Priority 는 충돌 해소 방식이다.
	// +kubebuilder:validation:Enum=replica;snapshot;no_sync
	// +kubebuilder:default="snapshot"
	// +optional
	Priority string `json:"priority,omitempty"`
}

// RestoreSource 는 한 peer 가 읽을 스냅샷 위치다.
type RestoreSource struct {
	// Peer 는 복원을 실행할 STS 서수다.
	Peer int32 `json:"peer"`
	// Location 은 **그 peer 가 직접 닿을 수 있는** URL 또는 로컬 파일 경로다.
	Location string `json:"location"`
}

// QdrantRestoreStatus 는 관측된 상태다.
type QdrantRestoreStatus struct {
	// +optional
	Phase string `json:"phase,omitempty"`
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Pending 은 남은 peer 복원이다 — 여기 있어서 재기동해도 이어진다.
	// +optional
	Pending []RestoreSource `json:"pending,omitempty"`

	// Restored 는 발행을 마친 peer 복원이다.
	// +optional
	Restored []RestoreSource `json:"restored,omitempty"`

	// +optional
	CompletionTime *metav1.Time `json:"completionTime,omitempty"`

	// +patchMergeKey=type
	// +patchStrategy=merge
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Cluster",type=string,JSONPath=".spec.clusterRef"
// +kubebuilder:printcolumn:name="Collection",type=string,JSONPath=".spec.collection"
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="Completed",type=date,JSONPath=".status.completionTime"

// QdrantRestore is the Schema for the qdrantrestores API
type QdrantRestore struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of QdrantRestore
	// +required
	Spec QdrantRestoreSpec `json:"spec"`

	// status defines the observed state of QdrantRestore
	// +optional
	Status QdrantRestoreStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// QdrantRestoreList contains a list of QdrantRestore
type QdrantRestoreList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []QdrantRestore `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &QdrantRestore{}, &QdrantRestoreList{})
		return nil
	})
}
