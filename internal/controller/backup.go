/*
Copyright 2026 Keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package controller

import (
	"slices"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	qdrantv1alpha1 "github.com/keiailab/qdrant-operator/api/v1alpha1"
	"github.com/keiailab/qdrant-operator/internal/qdrant"
)

// ── C-3 백업 계획 — 순수 함수 ──
//
// 스냅샷은 노드 단위라 한 세대(generation)는 (컬렉션 × peer) 짝의 곱이다.
//
//	컬렉션 vec, docs / peer 0, 1  →  vec@0  vec@1  docs@0  docs@1
//
// 이 넷을 한 번에 발행하지 않는다. 스냅샷은 디스크·네트워크를 크게 쓰고, 동시에 돌리면
// 백업이 지키려던 서비스를 해친다 — 이동/복제와 같은 동시 1건 규율을 따른다.

const (
	// snapshotAppearDeadline: 발행 후 스냅샷이 목록에 나타나야 하는 기한. 이동(60s)보다
	// 훨씬 길다 — 큰 컬렉션의 스냅샷은 수십 분이 걸린다(그래서 wait=false 로 발행한다).
	snapshotAppearDeadline = 30 * time.Minute

	phaseBackupCreating = "Creating"
	phaseBackupReady    = "Ready"
	phaseBackupIdle     = "Idle"
	phaseBackupDegraded = "Degraded"

	reasonSnapshotFailed = "SnapshotFailed"
	reasonScheduleValid  = "ScheduleInvalid"
	reasonBackupDone     = "BackupCompleted"
)

// cronParser 는 5필드 표준 cron 이다(초 필드 없음) — kubernetes CronJob 과 같은 표기.
var cronParser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)

// planGeneration 은 한 세대의 작업 목록을 만든다. 순서는 컬렉션명 → peer 서수 오름차순
// 으로 고정한다 — 같은 입력이면 같은 순서라 중단 후 재개가 예측 가능하다.
func planGeneration(collections []string, peers int32) []qdrantv1alpha1.PendingSnapshot {
	if peers < 1 {
		return nil
	}

	sorted := slices.Clone(collections)
	slices.Sort(sorted)

	var plan []qdrantv1alpha1.PendingSnapshot
	for _, coll := range sorted {
		for ordinal := range peers {
			plan = append(plan, qdrantv1alpha1.PendingSnapshot{Collection: coll, Peer: ordinal})
		}
	}
	return plan
}

// newSnapshotName 은 발행 전 관측(known)에 없던 이름을 찾는다. 이것이 완료 판정이다 —
// wait=false 응답에는 이름이 없어서 관측으로만 알 수 있다.
//
// known 을 기준으로 삼는 이유: 목록에는 남이 만든 스냅샷과 지난 세대의 것이 섞여 있다.
// "가장 새것"을 고르면 그것들을 자기 산출물로 오인한다.
func newSnapshotName(before []string, now []qdrant.SnapshotInfo) (qdrant.SnapshotInfo, bool) {
	var found []qdrant.SnapshotInfo
	for _, s := range now {
		if !slices.Contains(before, s.Name) {
			found = append(found, s)
		}
	}
	if len(found) == 0 {
		return qdrant.SnapshotInfo{}, false
	}

	// 둘 이상이면(동시에 누가 더 만들었다면) 이름 오름차순 첫 번째 — 결정론이 우선이다.
	slices.SortFunc(found, func(a, b qdrant.SnapshotInfo) int { return strings.Compare(a.Name, b.Name) })
	return found[0], true
}

// prunable 은 보존기간을 넘긴 스냅샷 이름을 오래된 순으로 돌려준다.
//
// 대상은 그 (컬렉션, peer) 의 **전체** 스냅샷이지 이 CR 이 만든 것만이 아니다. 보존기간은
// 보관량을 정하는 정책이고, 손으로 만든 스냅샷만 무한정 쌓이면 정책이 아니게 된다.
// 그래서 retention 은 명시적으로 선언해야만 켜진다(미지정 = 아무것도 지우지 않음).
func prunable(snapshots []qdrant.SnapshotInfo, keepLast int32) []string {
	if keepLast < 1 || int32(len(snapshots)) <= keepLast {
		return nil
	}

	// 새것이 앞으로. creation_time 은 qdrant 가 비워 보낼 수 있어 이름을 tie-break 로 쓴다
	// (qdrant 의 스냅샷 이름은 시각을 포함해 사전순이 곧 시간순이다).
	ordered := slices.Clone(snapshots)
	slices.SortFunc(ordered, func(a, b qdrant.SnapshotInfo) int {
		if a.CreationTime != b.CreationTime {
			return strings.Compare(b.CreationTime, a.CreationTime)
		}
		return strings.Compare(b.Name, a.Name)
	})

	var out []string
	for _, s := range ordered[keepLast:] {
		out = append(out, s.Name)
	}
	return out
}

// nextRun 은 cron 표기의 다음 실행 시각이다. 표기가 틀리면 두 번째 반환값이 false 다 —
// 조용히 안 도는 백업을 만들지 않기 위해 호출자가 Degraded 로 표면화한다.
func nextRun(schedule string, after time.Time) (time.Time, bool) {
	if schedule == "" {
		return time.Time{}, false
	}
	sched, err := cronParser.Parse(schedule)
	if err != nil {
		return time.Time{}, false
	}
	return sched.Next(after), true
}

// dueNow 는 예약이 지금 발동해야 하는지다. 한 번도 안 돈 예약은 즉시 발동한다 —
// 백업을 선언하고 다음 창까지 아무 백업도 없는 상태로 두지 않는다.
func dueNow(bk *qdrantv1alpha1.QdrantBackup, now time.Time) bool {
	if bk.Spec.Suspend || bk.Spec.Schedule == "" {
		return false
	}
	if bk.Status.LastSuccessTime == nil {
		return true
	}

	next, ok := nextRun(bk.Spec.Schedule, bk.Status.LastSuccessTime.Time)
	return ok && !next.After(now)
}

// oneShotDue 는 예약 없는 CR 이 아직 한 번도 안 돌았는지다.
func oneShotDue(bk *qdrantv1alpha1.QdrantBackup) bool {
	return bk.Spec.Schedule == "" && bk.Status.LastSuccessTime == nil && !bk.Spec.Suspend
}

// setNextSchedule 은 다음 실행 시각을 status 에 적는다(예약이 없으면 비운다).
func setNextSchedule(bk *qdrantv1alpha1.QdrantBackup, now time.Time) {
	if bk.Spec.Schedule == "" || bk.Spec.Suspend {
		bk.Status.NextScheduleTime = nil
		return
	}

	if next, ok := nextRun(bk.Spec.Schedule, now); ok {
		t := metav1.NewTime(next)
		bk.Status.NextScheduleTime = &t
	}
}
