/*
Copyright 2026 Keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package controller

import (
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/keiailab/qdrant-operator/internal/qdrant"
)

func TestClusterMetrics_관측반영(t *testing.T) {
	o := healthyObs(3)
	setState(o, 0, 2, qdrant.ShardStateDead)

	clusterMetrics("data", "m1", o, 4, true)
	defer forgetCluster("data", "m1")

	if got := testutil.ToFloat64(peersGauge.WithLabelValues("data", "m1")); got != 3 {
		t.Fatalf("peers=%v", got)
	}
	if got := testutil.ToFloat64(deadReplicas.WithLabelValues("data", "m1")); got != 1 {
		t.Fatalf("dead=%v", got)
	}
	if got := testutil.ToFloat64(plannedMoves.WithLabelValues("data", "m1")); got != 4 {
		t.Fatalf("planned=%v", got)
	}
	if got := testutil.ToFloat64(upgradeInProgress.WithLabelValues("data", "m1")); got != 1 {
		t.Fatalf("upgrade=%v", got)
	}
}

// 관측이 실패했을 때 게이지를 0 으로 덮으면 "Dead 0건"과 "모르겠음"이 같은 값이 된다.
// 알럿은 그 둘을 구별해야 한다.
func TestClusterMetrics_관측실패는덮지않는다(t *testing.T) {
	clusterMetrics("data", "m2", healthyObsWithDead(), 0, false)
	defer forgetCluster("data", "m2")
	if got := testutil.ToFloat64(deadReplicas.WithLabelValues("data", "m2")); got != 1 {
		t.Fatalf("사전 조건 실패: dead=%v", got)
	}

	clusterMetrics("data", "m2", nil, 0, false)
	if got := testutil.ToFloat64(deadReplicas.WithLabelValues("data", "m2")); got != 1 {
		t.Fatalf("관측 실패가 지난 값을 덮었다: dead=%v", got)
	}
}

func TestForgetCluster_시계열회수(t *testing.T) {
	clusterMetrics("data", "m3", healthyObs(2), 1, false)
	shardOperations.WithLabelValues("data", "m3", "Rebalance").Inc()
	shardOperationFailures.WithLabelValues("data", "m3").Inc()

	forgetCluster("data", "m3")

	// 남아 있으면 사라진 클러스터의 마지막 값이 영원히 알럿을 울린다.
	for name, count := range map[string]int{
		"qdrant_operator_peers":                  testutil.CollectAndCount(peersGauge),
		"qdrant_operator_planned_moves":          testutil.CollectAndCount(plannedMoves),
		"qdrant_operator_shard_operations_total": testutil.CollectAndCount(shardOperations),
	} {
		if count != 0 {
			t.Fatalf("%s 시계열이 %d건 남음", name, count)
		}
	}
}

func TestMetrics_이름과도움말(t *testing.T) {
	// 알럿 규칙이 이름에 걸리므로 오타는 조용한 침묵으로 나타난다.
	clusterMetrics("data", "m4", healthyObs(1), 0, false)
	defer forgetCluster("data", "m4")

	want := `# HELP qdrant_operator_peers Peers currently in Raft consensus.
# TYPE qdrant_operator_peers gauge
qdrant_operator_peers{name="m4",namespace="data"} 1
`
	if err := testutil.CollectAndCompare(peersGauge, strings.NewReader(want)); err != nil {
		t.Fatal(err)
	}
}

func healthyObsWithDead() *observation {
	o := healthyObs(3)
	setState(o, 0, 2, qdrant.ShardStateDead)
	return o
}
