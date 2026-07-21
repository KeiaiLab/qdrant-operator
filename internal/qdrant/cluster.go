package qdrant

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
)

// ── 분포/클러스터 타입 (라이브 실측 스키마 바인딩, 2026-07-20 v1.18.2) ──
//
// 주의: peer_id 는 int32 범위를 초과하는 number(실측 2.29e15)라 반드시 uint64.
// GET /cluster 의 peers 는 "peer_id 문자열" 을 키로 하는 map 이다.

// Peer 는 클러스터 합의 멤버.
type Peer struct {
	ID  uint64
	URI string // 예: http://<name>-N.<name>-headless:6335/ (trailing slash 포함)
}

// ClusterInfo 는 GET /cluster 관측 결과.
type ClusterInfo struct {
	Enabled           bool
	PeerID            uint64 // 응답한 노드 자신의 peer id
	Peers             []Peer // peer id 오름차순 정렬(결정론)
	RaftLeader        uint64
	PendingOperations int
}

// ShardInfo 는 컬렉션의 shard 1개 배치 상태. local_shards 항목에는 peer_id 가 없어
// 응답의 result.peer_id(로컬 피어)로 귀속시킨다.
type ShardInfo struct {
	ShardID     uint32
	PeerID      uint64
	State       string // Active | Initializing | ...
	PointsCount uint64 // remote 는 미제공(0)
}

// TransferInfo 는 진행 중 shard 전송.
type TransferInfo struct {
	ShardID uint32
	From    uint64
	To      uint64
	Method  string
}

// CollectionClusterInfo 는 GET /collections/{c}/cluster 관측 결과.
type CollectionClusterInfo struct {
	PeerID     uint64
	ShardCount int
	Shards     []ShardInfo // local + remote 통합
	Transfers  []TransferInfo
}

// TransferMethodStreamRecords — move_shard 기본 전송 방법. 인덱스는 타겟에서 재구축되지만
// 가장 견고하다(불안정 환경 권장 방법).
const TransferMethodStreamRecords = "stream_records"

// ── alias 원자 스왑 (POST /collections/aliases) ──

type CreateAlias struct {
	AliasName      string `json:"alias_name"`
	CollectionName string `json:"collection_name"`
}

type DeleteAlias struct {
	AliasName string `json:"alias_name"`
}

// AliasAction 은 update_aliases actions 항목 — 필드 중 하나만 설정한다.
type AliasAction struct {
	CreateAlias *CreateAlias `json:"create_alias,omitempty"`
	DeleteAlias *DeleteAlias `json:"delete_alias,omitempty"`
}

// ── envelope 공통 처리 ──
// 모든 qdrant REST 응답 = {"result": <payload>, "status": "ok"|<err>, "time": <float>}.

func (c *HTTPClient) doJSON(ctx context.Context, method, path string, reqBody any, result any) error {
	var rd io.Reader
	if reqBody != nil {
		buf, err := json.Marshal(reqBody)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, rd)
	if err != nil {
		return err
	}
	if reqBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.HC.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("%s %s: %s: %s", method, path, resp.Status, string(b))
	}
	if result == nil {
		return nil
	}
	envelope := struct {
		Result json.RawMessage `json:"result"`
	}{}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return err
	}
	return json.Unmarshal(envelope.Result, result)
}

// ── 관측 (B-2) ──

func (c *HTTPClient) ListCollections(ctx context.Context) ([]string, error) {
	var r struct {
		Collections []struct {
			Name string `json:"name"`
		} `json:"collections"`
	}
	if err := c.doJSON(ctx, http.MethodGet, "/collections", nil, &r); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(r.Collections))
	for _, col := range r.Collections {
		names = append(names, col.Name)
	}
	slices.Sort(names) // 서버 순서 비보장 — 관측 결정론(§5.1)
	return names, nil
}

func (c *HTTPClient) ClusterInfo(ctx context.Context) (*ClusterInfo, error) {
	var r struct {
		Status string `json:"status"`
		PeerID uint64 `json:"peer_id"`
		Peers  map[string]struct {
			URI string `json:"uri"`
		} `json:"peers"`
		RaftInfo struct {
			Leader            uint64 `json:"leader"`
			PendingOperations int    `json:"pending_operations"`
		} `json:"raft_info"`
	}
	if err := c.doJSON(ctx, http.MethodGet, "/cluster", nil, &r); err != nil {
		return nil, err
	}
	info := &ClusterInfo{
		Enabled:           r.Status == "enabled",
		PeerID:            r.PeerID,
		RaftLeader:        r.RaftInfo.Leader,
		PendingOperations: r.RaftInfo.PendingOperations,
	}
	for idStr, p := range r.Peers {
		id, err := strconv.ParseUint(idStr, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("peers 키 파싱(%q): %w", idStr, err)
		}
		info.Peers = append(info.Peers, Peer{ID: id, URI: p.URI})
	}
	slices.SortFunc(info.Peers, func(a, b Peer) int { return cmp.Compare(a.ID, b.ID) })
	return info, nil
}

func (c *HTTPClient) CollectionCluster(ctx context.Context, name string) (*CollectionClusterInfo, error) {
	var r struct {
		PeerID      uint64 `json:"peer_id"`
		ShardCount  int    `json:"shard_count"`
		LocalShards []struct {
			ShardID     uint32 `json:"shard_id"`
			PointsCount uint64 `json:"points_count"`
			State       string `json:"state"`
		} `json:"local_shards"`
		RemoteShards []struct {
			ShardID uint32 `json:"shard_id"`
			PeerID  uint64 `json:"peer_id"`
			State   string `json:"state"`
		} `json:"remote_shards"`
		ShardTransfers []struct {
			ShardID uint32 `json:"shard_id"`
			From    uint64 `json:"from"`
			To      uint64 `json:"to"`
			Method  string `json:"method"`
		} `json:"shard_transfers"`
	}
	if err := c.doJSON(ctx, http.MethodGet, "/collections/"+name+"/cluster", nil, &r); err != nil {
		return nil, err
	}
	cc := &CollectionClusterInfo{PeerID: r.PeerID, ShardCount: r.ShardCount}
	for _, s := range r.LocalShards {
		cc.Shards = append(cc.Shards, ShardInfo{ShardID: s.ShardID, PeerID: r.PeerID, State: s.State, PointsCount: s.PointsCount})
	}
	for _, s := range r.RemoteShards {
		cc.Shards = append(cc.Shards, ShardInfo{ShardID: s.ShardID, PeerID: s.PeerID, State: s.State})
	}
	for _, tr := range r.ShardTransfers {
		cc.Transfers = append(cc.Transfers, TransferInfo{ShardID: tr.ShardID, From: tr.From, To: tr.To, Method: tr.Method})
	}
	return cc, nil
}

// ── 이동/드레인/alias (B-3/B-4/B-5 실행 표면) ──

// 반복 리터럴 상수(goconst) — 이동/복제/드롭 바디 공용 키와 shard 상태.
const (
	keyShardID       = "shard_id"
	ShardStateActive = "Active"
)

// MoveShard 는 shard 1개를 from→to 로 이동한다(비동기 — 완료는 CollectionCluster 의
// Transfers 소멸 + 배치 확인으로 관측). method 는 stream_records 고정(견고성 우선).
func (c *HTTPClient) MoveShard(ctx context.Context, collection string, shardID uint32, from, to uint64) error {
	body := map[string]any{
		"move_shard": map[string]any{
			keyShardID:     shardID,
			"from_peer_id": from,
			"to_peer_id":   to,
			"method":       TransferMethodStreamRecords,
		},
	}
	return c.doJSON(ctx, http.MethodPost, "/collections/"+collection+"/cluster", body, nil)
}

// ReplicateShard 는 shard 복제본을 from→to 로 추가 생성한다(원본 잔존 — RF 수리용,
// 비동기 완료 관측은 MoveShard 와 동일). method 는 stream_records 고정.
func (c *HTTPClient) ReplicateShard(ctx context.Context, collection string, shardID uint32, from, to uint64) error {
	body := map[string]any{
		"replicate_shard": map[string]any{
			keyShardID:     shardID,
			"from_peer_id": from,
			"to_peer_id":   to,
			"method":       TransferMethodStreamRecords,
		},
	}
	return c.doJSON(ctx, http.MethodPost, "/collections/"+collection+"/cluster", body, nil)
}

// DropReplica 는 잉여 복제본을 제거한다(drop_replica). 마지막 active replica 는 서버가
// 거부하지만, 컨트롤러는 발행 전 활성 replica>=2 를 관측으로 사전 확인해야 한다.
func (c *HTTPClient) DropReplica(ctx context.Context, collection string, shardID uint32, peerID uint64) error {
	body := map[string]any{
		"drop_replica": map[string]any{keyShardID: shardID, "peer_id": peerID},
	}
	return c.doJSON(ctx, http.MethodPost, "/collections/"+collection+"/cluster", body, nil)
}

// RemovePeer 는 빈 peer 를 합의에서 제거한다. force 는 shard 잔존 시에도 제거(드레인
// 검증 후에만 사용해야 하며, 컨트롤러는 기본적으로 force=false 로 호출한다).
func (c *HTTPClient) RemovePeer(ctx context.Context, peerID uint64, force bool) error {
	path := "/cluster/peer/" + strconv.FormatUint(peerID, 10)
	if force {
		path += "?force=true"
	}
	return c.doJSON(ctx, http.MethodDelete, path, nil, nil)
}

// UpdateAliases 는 actions 를 원자적으로 적용한다(스왑 = delete+create 동시).
func (c *HTTPClient) UpdateAliases(ctx context.Context, actions []AliasAction) error {
	return c.doJSON(ctx, http.MethodPost, "/collections/aliases", map[string]any{"actions": actions}, nil)
}
