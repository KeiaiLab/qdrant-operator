/*
Copyright 2026 Keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package controller

import (
	"fmt"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	qdrantv1alpha1 "github.com/keiailab/qdrant-operator/api/v1alpha1"
	"github.com/keiailab/qdrant-operator/internal/qdrant"
)

func TestPlanGeneration_결정론순서(t *testing.T) {
	plan := planGeneration([]string{"vec", "docs"}, 2)
	want := []string{"docs@0", "docs@1", "vec@0", "vec@1"}

	if len(plan) != len(want) {
		t.Fatalf("작업 %d건: %+v", len(plan), plan)
	}
	for i, w := range want {
		got := fmt.Sprintf("%s@%d", plan[i].Collection, plan[i].Peer)
		if got != w {
			t.Fatalf("[%d] %s (want %s)", i, got, w)
		}
	}

	// peer 가 없으면 계획도 없다 — 빈 클러스터에 발행하지 않는다.
	if p := planGeneration([]string{"vec"}, 0); p != nil {
		t.Fatalf("peer 0 인데 계획 발행: %+v", p)
	}
}

func TestNewSnapshotName_발행전관측기준(t *testing.T) {
	before := []string{"old-1", "old-2"}
	now := []qdrant.SnapshotInfo{{Name: "old-1"}, {Name: "old-2"}, {Name: "fresh"}}

	got, ok := newSnapshotName(before, now)
	if !ok || got.Name != "fresh" {
		t.Fatalf("새 스냅샷 판정: %+v ok=%v", got, ok)
	}

	// 아직 안 나타났으면 완료가 아니다.
	if _, ok := newSnapshotName(before, []qdrant.SnapshotInfo{{Name: "old-1"}, {Name: "old-2"}}); ok {
		t.Fatal("변화가 없는데 완료로 판정")
	}

	// 지난 세대의 것이 가장 새것이어도 자기 산출물로 오인하면 안 된다.
	if _, ok := newSnapshotName([]string{"z-newest"}, []qdrant.SnapshotInfo{{Name: "z-newest"}}); ok {
		t.Fatal("남의 스냅샷을 자기 것으로 판정")
	}
}

func TestPrunable_보존기간(t *testing.T) {
	snaps := []qdrant.SnapshotInfo{
		{Name: "a", CreationTime: "2026-09-07T00:00:00"},
		{Name: "c", CreationTime: "2026-09-09T00:00:00"},
		{Name: "b", CreationTime: "2026-09-08T00:00:00"},
	}

	// 최신 2개를 남기면 가장 오래된 a 만 지운다.
	got := prunable(snaps, 2)
	if len(got) != 1 || got[0] != "a" {
		t.Fatalf("정리 대상: %+v", got)
	}

	// 보존 수가 보유 수 이상이면 아무것도 안 지운다.
	if got := prunable(snaps, 3); got != nil {
		t.Fatalf("여유가 있는데 정리: %+v", got)
	}
	// 0(미지정과 같은 뜻)은 정리하지 않는다 — 백업 삭제는 명시 선언에서만.
	if got := prunable(snaps, 0); got != nil {
		t.Fatalf("keepLast=0 인데 정리: %+v", got)
	}

	// creation_time 이 비어도 죽지 않고 이름 순으로 정렬한다.
	blank := []qdrant.SnapshotInfo{{Name: "a"}, {Name: "b"}, {Name: "c"}}
	if got := prunable(blank, 1); len(got) != 2 || got[0] != "b" || got[1] != "a" {
		t.Fatalf("빈 creation_time 정렬: %+v", got)
	}
}

func TestSchedule_발동판정(t *testing.T) {
	now := time.Date(2026, 9, 10, 3, 5, 0, 0, time.UTC)
	bk := &qdrantv1alpha1.QdrantBackup{}
	bk.Spec.Schedule = "0 3 * * *"

	// 한 번도 안 돈 예약은 즉시 발동한다 — 선언하고 다음 창까지 백업 0 인 상태를 만들지 않는다.
	if !dueNow(bk, now) {
		t.Fatal("첫 실행이 발동하지 않음")
	}

	// 오늘 3시에 이미 돌았으면 내일까지 기다린다.
	ran := metav1.NewTime(time.Date(2026, 9, 10, 3, 0, 0, 0, time.UTC))
	bk.Status.LastSuccessTime = &ran
	if dueNow(bk, now) {
		t.Fatal("같은 창에서 두 번 발동")
	}
	if !dueNow(bk, now.Add(24*time.Hour)) {
		t.Fatal("다음 날 창이 발동하지 않음")
	}

	// suspend 는 예약만 멈춘다.
	bk.Spec.Suspend = true
	if dueNow(bk, now.Add(24*time.Hour)) {
		t.Fatal("suspend 인데 발동")
	}
	bk.Spec.Suspend = false

	// 표기가 틀리면 조용히 안 도는 대신 발동하지 않고 호출자가 표면화한다.
	bk.Spec.Schedule = "매일 세시"
	if _, ok := nextRun(bk.Spec.Schedule, now); ok {
		t.Fatal("잘못된 cron 을 받아들임")
	}
	if dueNow(bk, now.Add(24*time.Hour)) {
		t.Fatal("잘못된 cron 으로 발동")
	}
}

func TestOneShot_한번만(t *testing.T) {
	bk := &qdrantv1alpha1.QdrantBackup{}
	if !oneShotDue(bk) {
		t.Fatal("1회성이 발동하지 않음")
	}

	ran := metav1.Now()
	bk.Status.LastSuccessTime = &ran
	if oneShotDue(bk) {
		t.Fatal("1회성이 두 번 발동")
	}
}
