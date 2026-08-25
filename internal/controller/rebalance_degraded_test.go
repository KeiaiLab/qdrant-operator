/*
Copyright 2026 Keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

// rebalance_degraded_test.go — Degraded(MoveFailed) 회수 회귀 가드.
//
// 왜 이 가드가 있나: 조건을 켜는 경로만 있고 끄는 경로가 없으면 운영자에게 상시 빨간불이
// 남아 진짜 고장을 가린다. 라이브 실측 2026-08-22~26 — 4일간 Degraded=True(MoveFailed)인데
// 전 21개 컬렉션이 green 이고 문제 샤드는 양 피어 모두 Active 였다.

package controller

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	qdrantv1alpha1 "github.com/keiailab/qdrant-operator/api/v1alpha1"
)

// degradedCluster 는 Degraded=True 가 주어진 사유로 켜져 있는 CR 을 만든다.
func degradedCluster(reason string) *qdrantv1alpha1.QdrantCluster {
	qc := &qdrantv1alpha1.QdrantCluster{}
	qc.Status.MoveBackoff = 3
	meta.SetStatusCondition(&qc.Status.Conditions, metav1.Condition{
		Type: condDegraded, Status: metav1.ConditionTrue, Reason: reason, Message: "설정",
	})
	return qc
}

func degradedStatus(qc *qdrantv1alpha1.QdrantCluster) (metav1.ConditionStatus, string) {
	c := meta.FindStatusCondition(qc.Status.Conditions, condDegraded)
	if c == nil {
		return "<없음>", ""
	}
	return c.Status, c.Reason
}

func TestClearMoveFailed_자기사유만회수(t *testing.T) {
	qc := degradedCluster(reasonMoveFailed)
	clearMoveFailed(qc)

	if st, rs := degradedStatus(qc); st != metav1.ConditionFalse || rs != reasonBalanced {
		t.Fatalf("MoveFailed 는 회수돼야 한다: status=%s reason=%s", st, rs)
	}
	if qc.Status.MoveBackoff != 0 {
		t.Errorf("회수 시 backoff 도 초기화돼야 한다: %d", qc.Status.MoveBackoff)
	}
}

func TestClearMoveFailed_타사유는불가침(t *testing.T) {
	// 조건은 켠 쪽이 끈다 — drain/immutable 신호를 리밸런서가 삼키면 안 된다.
	for _, reason := range []string{reasonDrainBlocked, "ImmutableFieldChanged"} {
		qc := degradedCluster(reason)
		clearMoveFailed(qc)

		if st, rs := degradedStatus(qc); st != metav1.ConditionTrue || rs != reason {
			t.Errorf("%s 는 보존돼야 한다: status=%s reason=%s", reason, st, rs)
		}
		if qc.Status.MoveBackoff != 3 {
			t.Errorf("%s: backoff 를 건드리면 안 된다: %d", reason, qc.Status.MoveBackoff)
		}
	}
}

// TestReconcileRebalance_균형이면Degraded회수 는 실제 진입점에서의 회수를 고정한다.
// 균형 관측(각 컬렉션 샤드 1개가 양 피어에 복제 — 라이브 형상)에서는 계획이 비고,
// 그 경로가 이전 MoveFailed 를 걷어야 한다.
func TestReconcileRebalance_균형이면Degraded회수(t *testing.T) {
	qc := degradedCluster(reasonMoveFailed)
	qc.Spec.Replicas = 2
	o := obs([]uint64{1, 2}, map[string]map[uint32][]uint64{"c": {0: {1, 2}}})
	o.RF = map[string]uint32{"c": 2}

	r := &QdrantClusterReconciler{}
	phase, _ := r.reconcileRebalance(context.Background(), qc, o, nil)

	if phase != phaseRunning {
		t.Fatalf("균형이면 Running: %s", phase)
	}
	if st, rs := degradedStatus(qc); st != metav1.ConditionFalse || rs != reasonBalanced {
		t.Fatalf("균형 도달 시 MoveFailed 회수 실패: status=%s reason=%s", st, rs)
	}
}
