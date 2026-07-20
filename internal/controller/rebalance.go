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
	"fmt"
	"slices"
	"strings"

	"github.com/keiailab/qdrant-operator/internal/qdrant"
)

// ── B-3 rebalance planner — 순수 함수 (관측 스냅샷 → 결정론 이동 계획) ──
//
// 설계 규칙(불변):
//   - 이동은 count[donor]-count[recipient] >= 2 일 때만 후보가 된다 → 각 이동이 Σcount²
//     를 엄격 감소시켜 유한 종료가 보장된다. 균형 목표 = 컬렉션별 max-min <= 1.
//   - distinct-peer 제약: recipient 가 이미 그 shard 의 replica 를 보유하면 후보에서 제외.
//   - 결정론 총순서: donor count 내림차순 → recipient count 오름차순 → donor id 내림차순
//     → recipient id 오름차순 → shard id 오름차순 → 컬렉션명 오름차순(최종 tie-break).
//     동일 관측이면 항상 동일한 계획이 나온다(무저장 재계획의 전제).
//   - planner 는 순수 관측 입력만 받고 어떤 호출도 하지 않는다 — 발행은 executor 몫.

// plannedMove 는 이동 후보 1건.
type plannedMove struct {
	Collection string
	ShardID    uint32
	From       uint64
	To         uint64
	// 정렬 키 스냅샷(후보 생성 시점의 donor/recipient shard 수)
	fromCount int
	toCount   int
}

// String 은 status.plannedMoves 표기("collection/shard: from->to").
func (m plannedMove) String() string {
	return fmt.Sprintf("%s/%d: %d->%d", m.Collection, m.ShardID, m.From, m.To)
}

// observation 은 planner/executor 가 소비하는 관측 스냅샷(원시 peer id 유지).
type observation struct {
	Peers []qdrant.Peer // id 오름차순
	// 컬렉션명 → 분포. Transfers 가 하나라도 있으면 클러스터 전역에서 새 이동 발행 금지.
	Collections map[string]*qdrant.CollectionClusterInfo
}

// transfersInFlight 는 전 컬렉션의 진행 중 전송 수 합.
func (o *observation) transfersInFlight() int {
	n := 0
	for _, cc := range o.Collections {
		n += len(cc.Transfers)
	}
	return n
}

// allShardsActive 는 비-Active shard(전이 중) 존재 여부 — 있으면 이번 cycle 계획 보류.
func (o *observation) allShardsActive() bool {
	for _, cc := range o.Collections {
		for _, s := range cc.Shards {
			if s.State != "Active" {
				return false
			}
		}
	}
	return true
}

// planRebalance 는 관측에서 이동 계획 전체를 재산출한다(무저장 재계획 — 호출자는
// 첫 항목 하나만 집행한다). 반환 계획이 비면 균형(또는 distinct-peer 제약 하 잔여).
func planRebalance(obs *observation) []plannedMove {
	if len(obs.Peers) < 2 {
		return nil
	}
	var plan []plannedMove

	collNames := make([]string, 0, len(obs.Collections))
	for name := range obs.Collections {
		collNames = append(collNames, name)
	}
	slices.Sort(collNames)

	for _, coll := range collNames {
		cc := obs.Collections[coll]
		// peer 별 shard 수 (0 포함 — 모든 합의 peer 가 후보)
		count := map[uint64]int{}
		for _, p := range obs.Peers {
			count[p.ID] = 0
		}
		// shard → 보유 peer 집합 (replica 인지 — distinct-peer 제약용)
		holders := map[uint32]map[uint64]bool{}
		for _, s := range cc.Shards {
			if _, known := count[s.PeerID]; known {
				count[s.PeerID]++
			}
			if holders[s.ShardID] == nil {
				holders[s.ShardID] = map[uint64]bool{}
			}
			holders[s.ShardID][s.PeerID] = true
		}

		// donor(초과) → recipient(부족) 후보 열거
		for _, donor := range obs.Peers {
			for _, recip := range obs.Peers {
				if donor.ID == recip.ID || count[donor.ID]-count[recip.ID] < 2 {
					continue
				}
				// donor 가 보유한 shard 중 recipient 에 replica 가 없는 최소 shard id
				var shardIDs []uint32
				for _, s := range cc.Shards {
					if s.PeerID == donor.ID && !holders[s.ShardID][recip.ID] {
						shardIDs = append(shardIDs, s.ShardID)
					}
				}
				if len(shardIDs) == 0 {
					continue // distinct-peer 제약으로 이 (donor,recip) 쌍은 이동 불가
				}
				slices.Sort(shardIDs)
				plan = append(plan, plannedMove{
					Collection: coll, ShardID: shardIDs[0],
					From: donor.ID, To: recip.ID,
					fromCount: count[donor.ID], toCount: count[recip.ID],
				})
			}
		}
	}

	// 결정론 총순서
	slices.SortFunc(plan, func(a, b plannedMove) int {
		if a.fromCount != b.fromCount {
			return b.fromCount - a.fromCount // donor count 내림차순
		}
		if a.toCount != b.toCount {
			return a.toCount - b.toCount // recipient count 오름차순
		}
		if a.From != b.From {
			if a.From > b.From { // donor id 내림차순
				return -1
			}
			return 1
		}
		if a.To != b.To {
			if a.To < b.To { // recipient id 오름차순
				return -1
			}
			return 1
		}
		if a.ShardID != b.ShardID {
			return int(a.ShardID) - int(b.ShardID) // shard id 오름차순
		}
		return strings.Compare(a.Collection, b.Collection) // 컬렉션명 오름차순
	})
	return plan
}
