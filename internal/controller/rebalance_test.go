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

package controller

import (
	"reflect"
	"testing"

	"github.com/keiailab/qdrant-operator/internal/qdrant"
)

// obs 는 테스트 관측 조립 헬퍼 — placement: 컬렉션 → shardID → 보유 peer 목록(replica).
func obs(peerIDs []uint64, placement map[string]map[uint32][]uint64) *observation {
	o := &observation{Collections: map[string]*qdrant.CollectionClusterInfo{}}
	for _, id := range peerIDs {
		o.Peers = append(o.Peers, qdrant.Peer{ID: id})
	}
	for coll, shards := range placement {
		cc := &qdrant.CollectionClusterInfo{}
		for sid, holders := range shards {
			for _, pid := range holders {
				cc.Shards = append(cc.Shards, qdrant.ShardInfo{ShardID: sid, PeerID: pid, State: "Active"})
			}
		}
		cc.ShardCount = len(shards)
		o.Collections[coll] = cc
	}
	return o
}

func TestPlanRebalance_단일피어와균형은빈계획(t *testing.T) {
	// 단일 peer — 이동 자체가 불가능
	if plan := planRebalance(obs([]uint64{1}, map[string]map[uint32][]uint64{
		"c": {0: {1}, 1: {1}, 2: {1}},
	})); len(plan) != 0 {
		t.Fatalf("단일 peer 는 빈 계획이어야 함: %+v", plan)
	}
	// max-min == 1 — 이미 균형 (2:1)
	if plan := planRebalance(obs([]uint64{1, 2}, map[string]map[uint32][]uint64{
		"c": {0: {1}, 1: {1}, 2: {2}},
	})); len(plan) != 0 {
		t.Fatalf("max-min<=1 은 빈 계획이어야 함: %+v", plan)
	}
}

func TestPlanRebalance_불균형첫이동_결정론(t *testing.T) {
	// peer 11 이 shard 3개 전부 보유, peer 22 빈 손 (scale-up 직후 전형)
	o := obs([]uint64{11, 22}, map[string]map[uint32][]uint64{
		"vec": {0: {11}, 1: {11}, 2: {11}},
	})
	plan := planRebalance(o)
	if len(plan) == 0 {
		t.Fatal("불균형인데 계획이 비어 있음")
	}
	first := plan[0]
	if first.From != 11 || first.To != 22 || first.ShardID != 0 || first.Collection != "vec" {
		t.Fatalf("첫 이동 결정론 위반: %+v", first)
	}
	if got := first.String(); got != "vec/0: 11->22" {
		t.Fatalf("표기: %s", got)
	}
	// 동일 관측 → 동일 계획 (결정론)
	plan2 := planRebalance(o)
	if !reflect.DeepEqual(plan, plan2) {
		t.Fatal("동일 관측이 다른 계획을 냄 — 결정론 위반")
	}
}

func TestPlanRebalance_차이1은이동안함(t *testing.T) {
	// 2:1 분포 — count 차 1 → 이동하면 오히려 진동. >=2 규칙 검증.
	plan := planRebalance(obs([]uint64{1, 2}, map[string]map[uint32][]uint64{
		"c": {0: {1}, 1: {1}, 2: {2}},
	}))
	if len(plan) != 0 {
		t.Fatalf(">=2 규칙 위반: %+v", plan)
	}
}

func TestPlanRebalance_종료성_시뮬레이션(t *testing.T) {
	// 6 shard 가 전부 peer 1 에 — 계획 첫 항목을 반복 적용하면 유한 단계 안에 균형 도달.
	placement := map[string]map[uint32][]uint64{
		"c": {0: {1}, 1: {1}, 2: {1}, 3: {1}, 4: {1}, 5: {1}},
	}
	peers := []uint64{1, 2, 3}
	for range 20 {
		plan := planRebalance(obs(peers, placement))
		if len(plan) == 0 {
			// 균형 검증: 2/2/2
			count := map[uint64]int{}
			for _, holders := range placement["c"] {
				count[holders[0]]++
			}
			for _, p := range peers {
				if count[p] != 2 {
					t.Fatalf("수렴했지만 균형 아님: %+v", count)
				}
			}
			return
		}
		mv := plan[0]
		placement["c"][mv.ShardID] = []uint64{mv.To} // move 적용 (원본 삭제)
	}
	t.Fatal("20 스텝 안에 수렴하지 않음 — 종료성 위반")
}

func TestPlanRebalance_DistinctPeer제약(t *testing.T) {
	// shard 0 이 peer 1·2 양쪽에 replica(RF=2) — peer 1(3 shard) → peer 2(1 shard) 로
	// 옮길 수 있는 건 shard 0 이 아니라(2 가 이미 보유) shard 1 이어야 한다.
	o := obs([]uint64{1, 2}, map[string]map[uint32][]uint64{
		"c": {0: {1, 2}, 1: {1}, 2: {1}},
	})
	plan := planRebalance(o)
	if len(plan) == 0 {
		t.Fatal("이동 가능한 후보(shard 1,2)가 있는데 빈 계획")
	}
	if plan[0].ShardID == 0 {
		t.Fatalf("recipient 가 이미 replica 보유한 shard 0 을 이동하려 함: %+v", plan[0])
	}
	// 전부 replica 로 막히면 강제 이동 없이 빈 계획이어야 한다.
	blocked := obs([]uint64{1, 2}, map[string]map[uint32][]uint64{
		"c": {0: {1, 2}, 1: {1, 2}, 2: {1, 2}, 3: {1, 2}},
	})
	if plan := planRebalance(blocked); len(plan) != 0 {
		t.Fatalf("distinct-peer 로 전부 막혔는데 이동 발행: %+v", plan)
	}
}

func TestObservation_게이트헬퍼(t *testing.T) {
	o := obs([]uint64{1, 2}, map[string]map[uint32][]uint64{"c": {0: {1}}})
	if o.transfersInFlight() != 0 || !o.allShardsActive() {
		t.Fatal("기본 관측 게이트 오판")
	}
	o.Collections["c"].Transfers = []qdrant.TransferInfo{{ShardID: 0, From: 1, To: 2}}
	if o.transfersInFlight() != 1 {
		t.Fatal("transfersInFlight 미집계")
	}
	o.Collections["c"].Shards[0].State = "Initializing"
	if o.allShardsActive() {
		t.Fatal("비-Active shard 를 놓침")
	}
}
