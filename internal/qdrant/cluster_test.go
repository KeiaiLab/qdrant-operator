package qdrant

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// 라이브 클러스터 실측 원문(2026-07-20, v1.18.2)을 파싱 golden 으로 고정한다 —
// envelope {result,status,time} / peer_id 는 int32 초과 number / peers 는 문자열키 map.
const liveClusterJSON = `{"result":{"status":"enabled","peer_id":2293295724469674,"peers":{"2293295724469674":{"uri":"http://platform-data-qdrant-0.platform-data-qdrant-headless:6335/"}},"raft_info":{"term":2,"commit":128,"pending_operations":0,"leader":2293295724469674,"role":"Leader","is_voter":true},"consensus_thread_status":{"consensus_thread_status":"working","last_update":"2026-07-20T13:30:19.639756903Z"},"message_send_failures":{}},"status":"ok","time":7.124e-6}`

const liveCollectionClusterJSON = `{"result":{"peer_id":2293295724469674,"shard_count":1,"local_shards":[{"shard_id":0,"points_count":190,"state":"Active"}],"remote_shards":[],"shard_transfers":[]},"status":"ok","time":0.000116356}`

// 2-peer + 원격 shard + 진행 중 transfer 합성 케이스(remote/transfer 파싱 경로 검증).
const twoPeerCollectionClusterJSON = `{"result":{"peer_id":100,"shard_count":2,"local_shards":[{"shard_id":0,"points_count":10,"state":"Active"}],"remote_shards":[{"shard_id":1,"peer_id":200,"state":"Active"}],"shard_transfers":[{"shard_id":1,"from":200,"to":100,"sync":true,"method":"stream_records"}]},"status":"ok","time":0.0001}`

func newTestServer(t *testing.T, path, body string, capture *map[string]any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != path {
			// 요청 바디 캡처 대상(쓰기 엔드포인트)이 아니면 404
			http.NotFound(w, r)
			return
		}
		if capture != nil && r.Body != nil {
			_ = json.NewDecoder(r.Body).Decode(capture)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
}

func TestClusterInfo_파싱(t *testing.T) {
	srv := newTestServer(t, "/cluster", liveClusterJSON, nil)
	defer srv.Close()
	c := NewHTTPClient(srv.URL)

	info, err := c.ClusterInfo(context.Background())
	if err != nil {
		t.Fatalf("ClusterInfo: %v", err)
	}
	if info.PeerID != 2293295724469674 {
		t.Fatalf("peer_id uint64 파싱 실패: %d", info.PeerID)
	}
	if len(info.Peers) != 1 || info.Peers[0].ID != 2293295724469674 {
		t.Fatalf("peers map→slice 변환 실패: %+v", info.Peers)
	}
	if info.Peers[0].URI != "http://platform-data-qdrant-0.platform-data-qdrant-headless:6335/" {
		t.Fatalf("uri: %s", info.Peers[0].URI)
	}
	if info.RaftLeader != 2293295724469674 || info.PendingOperations != 0 {
		t.Fatalf("raft_info: leader=%d pending=%d", info.RaftLeader, info.PendingOperations)
	}
}

func TestCollectionCluster_파싱_로컬단일(t *testing.T) {
	srv := newTestServer(t, "/collections/c1/cluster", liveCollectionClusterJSON, nil)
	defer srv.Close()
	c := NewHTTPClient(srv.URL)

	cc, err := c.CollectionCluster(context.Background(), "c1")
	if err != nil {
		t.Fatalf("CollectionCluster: %v", err)
	}
	if cc.PeerID != 2293295724469674 || cc.ShardCount != 1 {
		t.Fatalf("헤더 필드: %+v", cc)
	}
	// local_shards 에는 peer_id 가 없다 — result.peer_id(로컬)로 채워져야 한다.
	if len(cc.Shards) != 1 || cc.Shards[0].PeerID != 2293295724469674 || cc.Shards[0].ShardID != 0 {
		t.Fatalf("local shard 귀속 실패: %+v", cc.Shards)
	}
	if cc.Shards[0].State != "Active" || cc.Shards[0].PointsCount != 190 {
		t.Fatalf("shard 필드: %+v", cc.Shards[0])
	}
	if len(cc.Transfers) != 0 {
		t.Fatalf("transfers 는 비어야 함: %+v", cc.Transfers)
	}
}

func TestCollectionCluster_파싱_원격과전송(t *testing.T) {
	srv := newTestServer(t, "/collections/c2/cluster", twoPeerCollectionClusterJSON, nil)
	defer srv.Close()
	c := NewHTTPClient(srv.URL)

	cc, err := c.CollectionCluster(context.Background(), "c2")
	if err != nil {
		t.Fatalf("CollectionCluster: %v", err)
	}
	if len(cc.Shards) != 2 {
		t.Fatalf("local+remote 합산 2 여야 함: %+v", cc.Shards)
	}
	var remote *ShardInfo
	for i := range cc.Shards {
		if cc.Shards[i].PeerID == 200 {
			remote = &cc.Shards[i]
		}
	}
	if remote == nil || remote.ShardID != 1 {
		t.Fatalf("remote shard 파싱 실패: %+v", cc.Shards)
	}
	if len(cc.Transfers) != 1 || cc.Transfers[0].ShardID != 1 || cc.Transfers[0].From != 200 || cc.Transfers[0].To != 100 {
		t.Fatalf("transfer 파싱 실패: %+v", cc.Transfers)
	}
}

func TestMoveShard_요청바디(t *testing.T) {
	var captured map[string]any
	srv := newTestServer(t, "/collections/c1/cluster", `{"result":true,"status":"ok","time":0.1}`, &captured)
	defer srv.Close()
	c := NewHTTPClient(srv.URL)

	if err := c.MoveShard(context.Background(), "c1", 3, 111, 222); err != nil {
		t.Fatalf("MoveShard: %v", err)
	}
	ms, ok := captured["move_shard"].(map[string]any)
	if !ok {
		t.Fatalf("move_shard 키 누락: %+v", captured)
	}
	if ms["shard_id"].(float64) != 3 || ms["from_peer_id"].(float64) != 111 || ms["to_peer_id"].(float64) != 222 {
		t.Fatalf("move_shard 바디: %+v", ms)
	}
	if ms["method"].(string) != "stream_records" {
		t.Fatalf("기본 전송 방법은 stream_records 여야 함: %+v", ms)
	}
}

func TestRemovePeer_경로와쿼리(t *testing.T) {
	var gotPath, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery = r.URL.Path, r.URL.RawQuery
		if r.Method != http.MethodDelete {
			t.Errorf("DELETE 여야 함: %s", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"result":true,"status":"ok","time":0.1}`))
	}))
	defer srv.Close()
	c := NewHTTPClient(srv.URL)

	if err := c.RemovePeer(context.Background(), 2293295724469674, false); err != nil {
		t.Fatalf("RemovePeer: %v", err)
	}
	if gotPath != "/cluster/peer/2293295724469674" || gotQuery != "" {
		t.Fatalf("path=%s query=%s", gotPath, gotQuery)
	}
	if err := c.RemovePeer(context.Background(), 42, true); err != nil {
		t.Fatalf("RemovePeer force: %v", err)
	}
	if gotPath != "/cluster/peer/42" || gotQuery != "force=true" {
		t.Fatalf("force path=%s query=%s", gotPath, gotQuery)
	}
}

func TestUpdateAliases_요청바디(t *testing.T) {
	var captured map[string]any
	srv := newTestServer(t, "/collections/aliases", `{"result":true,"status":"ok","time":0.1}`, &captured)
	defer srv.Close()
	c := NewHTTPClient(srv.URL)

	err := c.UpdateAliases(context.Background(), []AliasAction{
		{CreateAlias: &CreateAlias{AliasName: "vec", CollectionName: "vec-rs-2"}},
		{DeleteAlias: &DeleteAlias{AliasName: "old"}},
	})
	if err != nil {
		t.Fatalf("UpdateAliases: %v", err)
	}
	actions, ok := captured["actions"].([]any)
	if !ok || len(actions) != 2 {
		t.Fatalf("actions 배열: %+v", captured)
	}
	first := actions[0].(map[string]any)["create_alias"].(map[string]any)
	if first["alias_name"] != "vec" || first["collection_name"] != "vec-rs-2" {
		t.Fatalf("create_alias 바디: %+v", first)
	}
}

func TestListCollections_파싱(t *testing.T) {
	srv := newTestServer(t, "/collections", `{"result":{"collections":[{"name":"a"},{"name":"b"}]},"status":"ok","time":0.1}`, nil)
	defer srv.Close()
	c := NewHTTPClient(srv.URL)

	names, err := c.ListCollections(context.Background())
	if err != nil {
		t.Fatalf("ListCollections: %v", err)
	}
	if len(names) != 2 || names[0] != "a" || names[1] != "b" {
		t.Fatalf("names: %+v", names)
	}
}
