/*
Copyright 2026 Keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package controller

import (
	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

// ── D-2 관측 지표 ──
//
// 넣는 기준 하나: **값이 변하면 사람이 무언가 해야 하는가.** 그 답이 아니오인 지표는
// 대시보드를 채울 뿐 아무도 보지 않게 되고, 그러면 진짜 신호도 같이 묻힌다.
// 샤드 분포처럼 "보면 좋은" 것들은 status 에 이미 있으므로 여기 옮기지 않는다.
//
// 라벨은 CR 좌표까지만 둔다 — 컬렉션·shard 를 라벨로 올리면 카디널리티가 클러스터 규모를
// 따라 늘어난다(실측 21컬렉션 × 샤드 × peer).

const (
	metricNS  = "qdrant_operator"
	labelNS   = "namespace"
	labelName = "name"
)

var (
	// backupLastSuccess — 백업이 조용히 멈춘 것을 아는 유일한 방법이다.
	// 알럿: time() - qdrant_operator_backup_last_success_timestamp_seconds > 26h
	backupLastSuccess = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: metricNS, Name: "backup_last_success_timestamp_seconds",
		Help: "Unix time of the last successful backup generation.",
	}, []string{labelNS, labelName})

	// backupSnapshots — 마지막 세대가 실제로 만든 스냅샷 수. 백업은 도는데 0 이면
	// 대상 컬렉션이 사라졌거나 선택이 잘못된 것이다(성공으로 보이는 실패).
	backupSnapshots = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: metricNS, Name: "backup_snapshots",
		Help: "Snapshots produced by the last successful backup generation.",
	}, []string{labelNS, labelName})

	// deadReplicas — 내구성이 깎인 상태. 0 이 아닌 채로 머물면 재복제가 진행하지 못하는 중이다.
	deadReplicas = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: metricNS, Name: "dead_replicas",
		Help: "Shard replicas observed in the Dead state.",
	}, []string{labelNS, labelName})

	// plannedMoves — 재배치 대기 깊이. 계속 0 이 아니면 수렴하지 못하는 것이고,
	// 그것은 status 를 열어 보기 전에는 조용하다.
	plannedMoves = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: metricNS, Name: "planned_moves",
		Help: "Shard operations the rebalancer intends to issue.",
	}, []string{labelNS, labelName})

	// peers — 합의 멤버 수. spec.replicas 와 어긋나면 peer 하나가 돌아오지 못한 것이다.
	peersGauge = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: metricNS, Name: "peers",
		Help: "Peers currently in Raft consensus.",
	}, []string{labelNS, labelName})

	// upgradeInProgress — 1 인 채로 오래 머물면 롤아웃이 건강 게이트를 통과하지 못하는 중이다.
	// 멈춘 업그레이드는 실패하지 않으므로 이 지표 없이는 아무도 모른다.
	upgradeInProgress = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: metricNS, Name: "upgrade_in_progress",
		Help: "1 while a StatefulSet rollout is being gated by the operator.",
	}, []string{labelNS, labelName})

	// shardOperations — 발행량. kind 는 Rebalance/Replicate/DeadRepair/Drain.
	shardOperations = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: metricNS, Name: "shard_operations_total",
		Help: "Shard operations issued, by kind.",
	}, []string{labelNS, labelName, "kind"})

	// shardOperationFailures — 발행 실패·유실. 증가율이 붙으면 재배치가 헛돌고 있다.
	shardOperationFailures = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: metricNS, Name: "shard_operation_failures_total",
		Help: "Shard operations that failed to issue or were never observed.",
	}, []string{labelNS, labelName})
)

func init() {
	metrics.Registry.MustRegister(
		backupLastSuccess, backupSnapshots,
		deadReplicas, plannedMoves, peersGauge, upgradeInProgress,
		shardOperations, shardOperationFailures,
	)
}

// clusterMetrics 는 한 클러스터의 게이지들을 관측값으로 맞춘다.
func clusterMetrics(namespace, name string, obs *observation, planned int, upgrading bool) {
	l := prometheus.Labels{labelNS: namespace, labelName: name}

	plannedMoves.With(l).Set(float64(planned))
	upgradeInProgress.With(l).Set(boolGauge(upgrading))

	if obs == nil {
		return // 관측 실패 — 지난 값을 0 으로 덮어쓰지 않는다(없는 것과 모르는 것은 다르다)
	}

	peersGauge.With(l).Set(float64(len(obs.Peers)))

	dead := 0
	for _, cc := range obs.Collections {
		for _, s := range cc.Shards {
			if s.State == shardStateDead {
				dead++
			}
		}
	}
	deadReplicas.With(l).Set(float64(dead))
}

// forgetCluster 는 삭제된 CR 의 시계열을 걷어낸다. 남겨두면 사라진 클러스터의 마지막 값이
// 영원히 고정돼 알럿을 울린다.
func forgetCluster(namespace, name string) {
	l := prometheus.Labels{labelNS: namespace, labelName: name}
	plannedMoves.Delete(l)
	peersGauge.Delete(l)
	deadReplicas.Delete(l)
	upgradeInProgress.Delete(l)
	shardOperationFailures.Delete(l)
	shardOperations.DeletePartialMatch(l)
}

// forgetBackup 은 삭제된 백업 CR 의 시계열을 걷어낸다.
func forgetBackup(namespace, name string) {
	l := prometheus.Labels{labelNS: namespace, labelName: name}
	backupLastSuccess.Delete(l)
	backupSnapshots.Delete(l)
}

func boolGauge(b bool) float64 {
	if b {
		return 1
	}
	return 0
}
