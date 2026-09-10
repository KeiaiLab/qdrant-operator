/*
Copyright 2026 Keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package controller

import (
	appsv1 "k8s.io/api/apps/v1"
)

// ── D-1 Raft-aware 롤링 업그레이드 ──
//
// StatefulSet 의 기본 롤링은 파드를 높은 서수부터 하나씩 갈아끼우고 다음으로 넘어가는 조건이
// **파드 Ready** 뿐이다. 분산 qdrant 에서 그것으로는 부족하다: 파드가 뜨고 /readyz 가 200 을
// 주는 시점과, 그 peer 가 합의에 복귀하고 자기 shard 가 Active 로 돌아온 시점이 다르다.
// 그 사이에 다음 파드를 내리면 **두 peer 가 동시에 불완전**해진다.
//
//	기본 롤링          파드N Ready → 파드N-1 즉시 종료
//	                              ↑ 이 시점 shard 는 아직 Initializing 일 수 있다
//
//	이 게이트          파드N Ready ∧ 합의 복귀 ∧ 전 shard Active ∧ 전송 0 → 파드N-1 진행
//
// 방법은 partition 이다. partition 이상 서수만 갱신되므로, 오퍼레이터가 그것을 한 칸씩
// 내리면 롤아웃 속도를 직접 쥔다. 정지 상태(롤아웃 없음)의 값은 0 이라 기존 렌더 산출물과
// 같다 — 업그레이드 중이 아닌 클러스터에서 이 기능은 관측되지 않는다.

// rolloutInProgress 는 STS 가 새 리비전으로 넘어가는 중인지다.
func rolloutInProgress(live *appsv1.StatefulSet) bool {
	if live == nil {
		return false
	}
	rev := live.Status.UpdateRevision
	return rev != "" && rev != live.Status.CurrentRevision
}

// upgradeReady 는 다음 서수로 넘어가도 되는지다 — 파드 준비와 클러스터 건강을 모두 본다.
//
// obs 가 nil 이면(관측 실패) 진행하지 않는다. 모르는 상태에서 파드를 더 내리는 것보다
// 롤아웃이 멈춰 있는 편이 낫다 — 멈춘 것은 status 로 보이고, 내려버린 것은 되돌릴 수 없다.
func upgradeReady(live *appsv1.StatefulSet, replicas int32, obs *observation) bool {
	if live == nil || obs == nil {
		return false
	}
	if live.Status.ReadyReplicas != replicas {
		return false
	}

	// 합의 복귀: 갱신된 peer 가 다시 멤버가 됐는가.
	if int32(len(obs.Peers)) != replicas {
		return false
	}

	// 데이터 복귀: 전이 중 shard 도, 진행 중 전송도 없어야 한다. Dead 는 여기서 막지 않는다 —
	// 그것은 재복제가 고칠 일이고, 업그레이드를 영구히 잠글 이유가 아니다(같은 함정 반복 금지).
	return !obs.transitioning() && obs.transfersInFlight() == 0
}

// upgradePartition 은 이번 회차에 적용할 partition 이다.
//
//	replicas=3, 아직 아무것도 안 바뀜(updated=0) → 2  (파드 2 만 갱신)
//	파드 2 가 건강해짐(updated=1)                → 1  (파드 1 진행)
//	건강하지 않음                                 → 현 진행도에서 동결
//	전부 갱신(updated=3)                          → 0  (롤아웃 완료)
func upgradePartition(live *appsv1.StatefulSet, replicas int32, obs *observation) int32 {
	if !rolloutInProgress(live) {
		return 0
	}

	frozen := max(replicas-live.Status.UpdatedReplicas, 0)

	if !upgradeReady(live, replicas, obs) {
		return frozen // 판정이 설 때까지 이 자리에 머문다
	}
	return max(frozen-1, 0)
}
