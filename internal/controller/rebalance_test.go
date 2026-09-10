/*
Copyright 2026 Keiailab.

Licensed under the MIT License. See the LICENSE file for details.
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

func TestPlanReplications_RF부족복제(t *testing.T) {
	// shard0 은 replica 1(부족), shard1 은 이미 RF 충족 — shard0 만 복제 계획.
	o := obs([]uint64{1, 2}, map[string]map[uint32][]uint64{
		"c": {0: {1}, 1: {1, 2}},
	})
	o.RF = map[string]uint32{"c": 2}
	plan := planReplications(o)
	if len(plan) != 1 {
		t.Fatalf("복제 계획 1건이어야 함: %+v", plan)
	}
	mv := plan[0]
	if !mv.Replicate || mv.ShardID != 0 || mv.From != 1 || mv.To != 2 {
		t.Fatalf("복제 후보: %+v", mv)
	}
	if got := mv.String(); got != "c/0: +replica 1->2" {
		t.Fatalf("표기: %s", got)
	}

	// peers < RF — 발행 불가(관측만).
	o1 := obs([]uint64{1}, map[string]map[uint32][]uint64{"c": {0: {1}}})
	o1.RF = map[string]uint32{"c": 2}
	if p := planReplications(o1); len(p) != 0 {
		t.Fatalf("peers<RF 인데 계획 발행: %+v", p)
	}

	// RF 1 — 대상 아님.
	o.RF["c"] = 1
	if p := planReplications(o); len(p) != 0 {
		t.Fatalf("RF<2 인데 계획 발행: %+v", p)
	}
}

func TestPlanSizeRebalance_크기2차기준(t *testing.T) {
	// count 균형(2:2)이지만 크기 극단 불균형 — 2차 단계가 발동해야 한다.
	o := obs([]uint64{1, 2}, map[string]map[uint32][]uint64{
		"c": {0: {1}, 1: {1}, 2: {2}, 3: {2}},
	})
	o.SizesComplete = true
	o.Sizes = map[string]map[uint32]uint64{"c": {0: 50_000, 1: 40_000, 2: 1_000, 3: 1_000}}
	plan := planRebalance(o) // count 후보 없음 → 크기 단계 진입 경로 검증
	if len(plan) != 1 {
		t.Fatalf("크기 이동 1건이어야 함: %+v", plan)
	}
	mv := plan[0]
	// shard1(40k) 이동 시 spread 90k-2k=88k → 8k, shard0(50k) 이동 시 12k — 효과 최대 후보.
	if mv.From != 1 || mv.To != 2 || mv.ShardID != 1 {
		t.Fatalf("효과 최대·결정론 후보(shard1, 1->2) 위반: %+v", mv)
	}

	// 관측 불완전 — 크기 단계 전체 스킵(안전 강등).
	o.SizesComplete = false
	if p := planRebalance(o); len(p) != 0 {
		t.Fatalf("SizesComplete=false 인데 크기 이동 발행: %+v", p)
	}

	// 임계 미달(절대차 9k < 10k) — 무행동.
	o.SizesComplete = true
	o.Sizes = map[string]map[uint32]uint64{"c": {0: 5_000, 1: 5_000, 2: 500, 3: 500}}
	if p := planRebalance(o); len(p) != 0 {
		t.Fatalf("임계 미달인데 이동 발행: %+v", p)
	}

	// 과교정 배제 — 통짜 shard 하나뿐이라 어떤 이동도 spread 를 줄이지 못하면 무행동.
	o.Sizes = map[string]map[uint32]uint64{"c": {0: 100_000, 1: 0, 2: 0, 3: 0}}
	if p := planRebalance(o); len(p) != 0 {
		t.Fatalf("개선 불가인데 이동 발행(진동 위험): %+v", p)
	}
}

// setState 는 조립된 관측에서 특정 (shard, peer) 의 상태를 바꾼다(컬렉션 "c" 고정).
func setState(o *observation, shardID uint32, peerID uint64, state string) {
	for i := range o.Collections["c"].Shards {
		s := &o.Collections["c"].Shards[i]
		if s.ShardID == shardID && s.PeerID == peerID {
			s.State = state
			return
		}
	}
}

func TestObservation_Dead는전이상태가아니다(t *testing.T) {
	// Dead 는 스스로 벗어나지 못하는 종착 상태다. 이것을 전이 중으로 분류하면 수리 루프가
	// 영원히 잠긴다 — 게이트는 Dead 를 통과시키되 진짜 전이 상태는 계속 막아야 한다.
	o := obs([]uint64{1, 2}, map[string]map[uint32][]uint64{"c": {0: {1, 2}}})
	setState(o, 0, 2, qdrant.ShardStateDead)

	if o.transitioning() {
		t.Fatal("Dead 를 전이 중으로 오분류 — 수리 계획에 도달하지 못한다")
	}
	if !o.hasDead() {
		t.Fatal("Dead 미검출")
	}

	setState(o, 0, 2, "Initializing")
	if !o.transitioning() {
		t.Fatal("진짜 전이 상태를 놓침")
	}
}

func TestPlanReplications_Dead보유피어는target에서제외(t *testing.T) {
	// shard0 의 Active 는 peer1 뿐이고 peer2 는 죽은 사본을 들고 있다. peer3 이 유일하게
	// 성한 후보다 — 죽은 사본이 있는 peer 로 복제를 쏘면 실패한다.
	o := obs([]uint64{1, 2, 3}, map[string]map[uint32][]uint64{"c": {0: {1, 2}}})
	setState(o, 0, 2, qdrant.ShardStateDead)
	o.RF = map[string]uint32{"c": 2}

	plan := planReplications(o)
	if len(plan) != 1 {
		t.Fatalf("RF 미달(Active 1 < 2)인데 복제 계획 1건이 아님: %+v", plan)
	}
	if plan[0].To == 2 {
		t.Fatalf("Dead 사본 보유 peer 2 를 target 으로 선택: %+v", plan[0])
	}
	if plan[0].From != 1 || plan[0].To != 3 || !plan[0].Replicate {
		t.Fatalf("복제 후보: %+v", plan[0])
	}
}

func TestPlanDeadDrops_RF충족후에만회수(t *testing.T) {
	// Active 2 = RF 2 충족 + Dead 1 잔존 → 회수 대상.
	o := obs([]uint64{1, 2, 3}, map[string]map[uint32][]uint64{"c": {0: {1, 2, 3}}})
	setState(o, 0, 3, qdrant.ShardStateDead)
	o.RF = map[string]uint32{"c": 2}

	plan := planDeadDrops(o)
	if len(plan) != 1 {
		t.Fatalf("Dead 회수 1건이어야 함: %+v", plan)
	}
	if !plan[0].Drop || plan[0].From != 3 || plan[0].ShardID != 0 {
		t.Fatalf("회수 후보: %+v", plan[0])
	}
	if got := plan[0].String(); got != "c/0: drop@3" {
		t.Fatalf("표기: %s", got)
	}

	// Active 1 < RF 2 — 내구성 미달 상태에서 드롭은 금지(먼저 재복제해야 한다).
	short := obs([]uint64{1, 2}, map[string]map[uint32][]uint64{"c": {0: {1, 2}}})
	setState(short, 0, 2, qdrant.ShardStateDead)
	short.RF = map[string]uint32{"c": 2}
	if p := planDeadDrops(short); len(p) != 0 {
		t.Fatalf("RF 미달인데 드롭 발행: %+v", p)
	}

	// Dead 없음 — 무행동.
	clean := obs([]uint64{1, 2}, map[string]map[uint32][]uint64{"c": {0: {1, 2}}})
	clean.RF = map[string]uint32{"c": 2}
	if p := planDeadDrops(clean); len(p) != 0 {
		t.Fatalf("Dead 가 없는데 드롭 발행: %+v", p)
	}
}

func TestPlanRebalance_Dead잔존시성능단계보류(t *testing.T) {
	// peer1 이 3 shard, peer2 가 0 — 평소라면 이동 후보다. 그러나 잔해가 남은 채로 센
	// count 는 오판이라 회수가 끝날 때까지 성능 단계는 멈춘다.
	o := obs([]uint64{1, 2}, map[string]map[uint32][]uint64{
		"c": {0: {1}, 1: {1}, 2: {1}},
	})
	setState(o, 2, 1, qdrant.ShardStateDead)

	if plan := planRebalance(o); len(plan) != 0 {
		t.Fatalf("Dead 잔존 중 리밸런스 발행: %+v", plan)
	}
}
