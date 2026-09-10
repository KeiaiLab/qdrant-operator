#!/usr/bin/env bash
# 문서-구현 괴리 게이트 — "구현했는데 문서는 예정이라고 말하는" 상태로 릴리스하지 못하게 한다.
#
# 왜 있나(2026-09-10 실측): Phase B 전체(컬렉션·리밸런스·drain·re-shard)와 KEDA 연동이
# 구현돼 라이브에서 21개 컬렉션을 돌리는 동안, README 4개 언어는 릴리스 세 번을 지나도록
# "Phase A only / QdrantCollection 은 예정" 이라고 말했다. 이 프로젝트의 핵심 가치가 자기
# 문서에 존재하지 않았다. 사람이 기억해야 하는 갱신은 반드시 밀린다.
#
# 검사 둘 다 기계적이다 — 판단을 요구하지 않는다.
#   1) 컨트롤러가 있는 Kind 를 README 가 "예정" 으로 표기하는가
#   2) 발행 채널 수 표기가 release.sh 의 실제 채널 수와 어긋나는가
#
# bash 3.2(맥 기본)에서도 돌아야 한다 — 연관배열 금지.
set -euo pipefail

cd "$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
fail=0

# 각 언어의 **상태로서의** "예정" 어휘. Kind 와 같은 줄에 있으면 괴리다.
# 한국어 "계획"·일본어 "計画" 단독은 제외한다 — "관측 → 계획 → move_shard" 처럼
# 상태가 아니라 일반 명사로 쓰이는 자리가 있어 오탐이 난다.
planned_words='planned|예정|計画中|予定|计划中'

# ── 1) 구현된 Kind 를 "예정" 이라 말하는 README ──
for controller in internal/controller/*_controller.go; do
	[[ -e "$controller" ]] || continue

	# 컨트롤러가 조정하는 Kind — SetupWithManager 의 For(&pkg.Kind{}) 가 정본이다.
	# For(&pkg.Kind{}) — 뒤에 builder 옵션이 더 붙는 형태가 있으므로 닫는 괄호는 보지 않는다.
	kind="$( { grep -oE 'For\(&[A-Za-z0-9_]+\.[A-Za-z]+\{\}' "$controller" || true; } |
		head -1 | sed -E 's/.*\.([A-Za-z]+)\{\}/\1/')"
	[[ -n "$kind" ]] || continue

	for readme in README.md README.*.md; do
		hits="$(grep -nEi "\`$kind\`.*($planned_words)" "$readme" || true)"
		[[ -n "$hits" ]] || continue

		echo "✗ $readme: \`$kind\` 를 예정으로 표기 — $controller 가 이미 있다" >&2
		printf '%s\n' "$hits" | sed 's/^/    /' >&2
		fail=1
	done
done

# ── 2) 발행 채널 수 ──
# release.sh 의 step 헤더 "(N채널)" 이 정본이다.
channels="$( { grep -oE '\([0-9]+채널\)' hack/release.sh || true; } | head -1 | tr -dc '0-9')"

# "숫자<탭>그 숫자를 뜻하는 표기들" — 정본과 다른 행이 README 에 있으면 괴리다.
channel_words='3	three channels|세 채널|3 つのチャネル|三个渠道|3채널
4	four channels|네 채널|4 つのチャネル|四个渠道|4채널
5	five channels|다섯 채널|5 つのチャネル|五个渠道|5채널'

if [[ -n "$channels" ]]; then
	while IFS=$'\t' read -r n words; do
		if [[ "$n" == "$channels" ]]; then
			continue # 정본과 같은 표기는 검사 대상이 아니다
		fi

		for readme in README.md README.*.md; do
			hits="$(grep -nEi "$words" "$readme" || true)"
			[[ -n "$hits" ]] || continue

			echo "✗ $readme: 채널 수 ${n} 로 표기 — release.sh 는 ${channels}채널이다" >&2
			printf '%s\n' "$hits" | sed 's/^/    /' >&2
			fail=1
		done
	done <<<"$channel_words"
fi

if ((fail)); then
	echo "" >&2
	echo "문서가 구현보다 뒤처졌다. 릴리스 전에 맞춘다 — 다음으로 미루면 또 밀린다." >&2
	exit 1
fi

echo "✓ doc-drift: 문서-구현 괴리 0건"
