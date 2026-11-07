/*
Copyright 2026 Keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package controller

import (
	"testing"
	"time"
)

// FuzzNextRun 은 사용자가 쓴 cron 문자열(spec.schedule)을 그대로 받는 nextRun 을 흔든다.
//
// 불변식:
//   - 어떤 입력에도 panic 하지 않는다.
//   - 받아들인 표기는 결정론이다 — 같은 입력을 다시 넣으면 같은 시각.
//   - 다음 실행은 기준 시각보다 엄밀히 뒤이고 분 경계에 맞는다(초 필드 없는 5필드 cron).
//     영 시각은 "5년 안에 발동 없음"(예: 2월 30일)이라 허용한다.
func FuzzNextRun(f *testing.F) {
	seeds := []string{
		"0 3 * * *",
		"*/15 * * * *",
		"0 0 1 1 *",
		"0 0 30 2 *",
		"@daily",
		"@every 1h",
		"5 4 * * sun",
		"0 0 * * * *",
		"",
		"not a cron",
	}
	for _, s := range seeds {
		f.Add(s)
	}

	after := time.Date(2026, time.October, 6, 12, 34, 56, 0, time.UTC)

	f.Fuzz(func(t *testing.T, schedule string) {
		next, ok := nextRun(schedule, after)
		if !ok {
			return
		}

		// 결정론 — 재파싱도 같은 답이어야 status.nextScheduleTime 이 흔들리지 않는다.
		again, okAgain := nextRun(schedule, after)
		if !okAgain || !again.Equal(next) {
			t.Fatalf("%q: 비결정 %v/%v vs %v/%v", schedule, ok, next, okAgain, again)
		}

		if next.IsZero() {
			return
		}
		if !next.After(after) {
			t.Fatalf("%q: next %v 가 기준 %v 보다 뒤가 아니다", schedule, next, after)
		}
		if next.Second() != 0 || next.Nanosecond() != 0 {
			t.Fatalf("%q: next %v 가 분 경계가 아니다", schedule, next)
		}
	})
}
