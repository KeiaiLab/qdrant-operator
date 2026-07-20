// Package qdrant 는 컨트롤러 ↔ Qdrant REST API 사이의 얇은 클라이언트 경계다.
// envtest 에는 실제 qdrant 가 없으므로 인터페이스 + Fake 로 컨트롤러 로직을 결정론
// 검증하고, 실 HTTP 구현은 e2e 가 커버한다. 메서드는 마일스톤이 실제로 쓰는 것만
// 추가한다 (B-1: 컬렉션 조회/생성/삭제).
package qdrant

import "context"

// CollectionSpec 은 컬렉션 생성 시 지정하는 파라미터 부분집합 — QdrantCollection CR 이
// 관리·검증하는 필드만 담는다.
type CollectionSpec struct {
	VectorSize        uint64
	Distance          string // Cosine | Dot | Euclid | Manhattan
	ShardNumber       uint32
	ReplicationFactor uint32
}

// CollectionInfo 는 라이브 컬렉션의 관측 상태. Exists=false 면 나머지 필드는 무의미.
type CollectionInfo struct {
	Exists            bool
	PointsCount       uint64
	VectorSize        uint64
	Distance          string
	ShardNumber       uint32
	ReplicationFactor uint32
}

// Client 는 B-1 이 필요로 하는 최소 표면. 이후 마일스톤(B-2 분포 관측, B-3 move_shard,
// B-4 remove peer, B-5 alias)에서 메서드를 증분 추가한다.
type Client interface {
	GetCollection(ctx context.Context, name string) (CollectionInfo, error)
	CreateCollection(ctx context.Context, name string, spec CollectionSpec) error
	DeleteCollection(ctx context.Context, name string) error
}
