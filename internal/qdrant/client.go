// Package qdrant 는 컨트롤러 ↔ Qdrant REST API 사이의 얇은 클라이언트 경계다.
// envtest 에는 실제 qdrant 가 없으므로 인터페이스 + Fake 로 컨트롤러 로직을 결정론
// 검증하고, 실 HTTP 구현은 e2e 가 커버한다. 메서드는 마일스톤이 실제로 쓰는 것만
// 추가한다 (B-1: 컬렉션 조회/생성/삭제).
package qdrant

import (
	"context"
	"encoding/json"
)

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

// Client 는 컨트롤러가 쓰는 qdrant 표면 전체 — B-1(컬렉션 수명주기) + B-2(관측) +
// B-3/B-4(이동·peer 제거) + B-5(alias 스왑).
type Client interface {
	// B-1 컬렉션 수명주기
	GetCollection(ctx context.Context, name string) (CollectionInfo, error)
	CreateCollection(ctx context.Context, name string, spec CollectionSpec) error
	DeleteCollection(ctx context.Context, name string) error
	// B-2 관측
	ListCollections(ctx context.Context) ([]string, error)
	ClusterInfo(ctx context.Context) (*ClusterInfo, error)
	CollectionCluster(ctx context.Context, name string) (*CollectionClusterInfo, error)
	// B-3/B-4 실행
	MoveShard(ctx context.Context, collection string, shardID uint32, from, to uint64) error
	ReplicateShard(ctx context.Context, collection string, shardID uint32, from, to uint64) error
	DropReplica(ctx context.Context, collection string, shardID uint32, peerID uint64) error
	RemovePeer(ctx context.Context, peerID uint64, force bool) error
	// B-5 alias + 데이터 복사
	UpdateAliases(ctx context.Context, actions []AliasAction) error
	ListAliases(ctx context.Context) (map[string]string, error)
	ScrollPoints(ctx context.Context, collection string, offset json.RawMessage, limit int) (points []json.RawMessage, next json.RawMessage, err error)
	UpsertPoints(ctx context.Context, collection string, points []json.RawMessage) error
}
