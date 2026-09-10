/*
Copyright 2026 Keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// QdrantBackupSpec 은 스냅샷 백업의 선언이다.
//
// 스냅샷은 노드 단위라, 컬렉션 하나의 온전한 백업은 전 peer 의 스냅샷 집합이다. 이 CR 하나가
// 그 팬아웃 전체를 한 세대(generation)로 묶는다.
type QdrantBackupSpec struct {
	// ClusterRef 는 같은 네임스페이스의 QdrantCluster 이름이다.
	ClusterRef string `json:"clusterRef"`

	// Collections 는 백업 대상이다. 비면 클러스터의 전 컬렉션 — 새 컬렉션이 생겨도
	// 백업 선언을 고치지 않아도 되는 쪽이 기본이다.
	// +optional
	Collections []string `json:"collections,omitempty"`

	// Schedule 은 5필드 cron 이다(예: "0 3 * * *"). 비면 1회성 — 한 번 돌고 Ready 에 머문다.
	// +optional
	Schedule string `json:"schedule,omitempty"`

	// Retention 미지정이면 아무것도 지우지 않는다. 백업을 지우는 것은 파괴 동작이라
	// 명시적 선언 없이는 하지 않는다.
	// +optional
	Retention *RetentionSpec `json:"retention,omitempty"`

	// Suspend 는 예약 실행만 멈춘다. 진행 중인 세대는 끝까지 간다 —
	// 중간에 끊긴 백업은 없느니만 못하다.
	// +optional
	Suspend bool `json:"suspend,omitempty"`
}

// RetentionSpec 은 (컬렉션, peer) 짝마다 남길 스냅샷 수다.
type RetentionSpec struct {
	// KeepLast 는 최신 몇 개를 남길지다. 0 은 "정리 안 함"이며, 이것이 미지정과 같은 뜻이다.
	// +kubebuilder:validation:Minimum=0
	// +optional
	KeepLast int32 `json:"keepLast,omitempty"`
}

// SnapshotRef 는 만들어진 스냅샷 하나의 좌표다.
type SnapshotRef struct {
	Collection string `json:"collection"`
	// Peer 는 STS 서수다 — 스냅샷은 그 peer 에만 존재한다.
	Peer int32  `json:"peer"`
	Name string `json:"name"`
	// +optional
	CreatedAt string `json:"createdAt,omitempty"`
}

// PendingSnapshot 은 아직 발행하지 않았거나 완료를 기다리는 작업 하나다.
type PendingSnapshot struct {
	Collection string `json:"collection"`
	Peer       int32  `json:"peer"`
	// IssuedAt 이 있으면 발행 완료 — 관측으로 완료를 기다리는 중이다.
	// +optional
	IssuedAt *metav1.Time `json:"issuedAt,omitempty"`
	// Known 은 발행 직전에 관측한 스냅샷 이름들이다. 새로 나타난 이름을 가려내는 기준이며,
	// 이것이 없으면 남이 만든 스냅샷을 자기 것으로 착각한다.
	// +optional
	Known []string `json:"known,omitempty"`
}

// QdrantBackupStatus 는 관측된 상태다.
type QdrantBackupStatus struct {
	// +optional
	Phase string `json:"phase,omitempty"`
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Active 는 발행 중인 스냅샷 1건 — 동시 1건 lane 이다. 스냅샷은 디스크·네트워크를
	// 크게 쓰므로 동시 다발은 서비스에 해롭다(이동/복제와 같은 규율).
	// +optional
	Active *PendingSnapshot `json:"active,omitempty"`

	// Pending 은 이번 세대의 남은 작업이다. 여기 있기 때문에 오퍼레이터가 재기동해도
	// 세대가 중간에서 이어진다.
	// +optional
	Pending []PendingSnapshot `json:"pending,omitempty"`

	// Snapshots 는 마지막으로 성공한 세대의 산출물이다.
	// +optional
	Snapshots []SnapshotRef `json:"snapshots,omitempty"`

	// +optional
	LastSuccessTime *metav1.Time `json:"lastSuccessTime,omitempty"`
	// NextScheduleTime 은 다음 실행 예정 시각 — 예약이 살아 있는지 status 만 보고 안다.
	// +optional
	NextScheduleTime *metav1.Time `json:"nextScheduleTime,omitempty"`

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
// +kubebuilder:printcolumn:name="Schedule",type=string,JSONPath=".spec.schedule"
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="Last",type=date,JSONPath=".status.lastSuccessTime"
// +kubebuilder:printcolumn:name="Next",type=string,JSONPath=".status.nextScheduleTime"

// QdrantBackup is the Schema for the qdrantbackups API
type QdrantBackup struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of QdrantBackup
	// +required
	Spec QdrantBackupSpec `json:"spec"`

	// status defines the observed state of QdrantBackup
	// +optional
	Status QdrantBackupStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// QdrantBackupList contains a list of QdrantBackup
type QdrantBackupList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []QdrantBackup `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &QdrantBackup{}, &QdrantBackupList{})
		return nil
	})
}
