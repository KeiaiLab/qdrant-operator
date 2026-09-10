#!/usr/bin/env bash
# 차트의 CRD 번들을 controller-gen 산출물에서 다시 만든다.
#
# 왜 있나(2026-09-10 실측): deploy/chart/templates/crd.yaml 은 config/crd/bases/*.yaml 을
# **손으로 이어붙인** 파일이었다. spec 에 필드를 하나 더한 직후 확인해 보니 차트 쪽만
# 옛 스키마였다 — 그대로 발행했다면 차트로 설치한 사용자의 CRD 가 새 필드를 거부한다.
# 오퍼레이터는 멀쩡히 돌고 사용자의 CR 만 조용히 반려되는, 진단하기 나쁜 형태다.
#
# 사용: hack/sync-chart-crds.sh          갱신
#       hack/sync-chart-crds.sh --check  어긋나면 실패(CI·릴리스 게이트)
set -euo pipefail

cd "$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

bundle="deploy/chart/templates/crd.yaml"
tmp="$(mktemp)"
trap 'rm -f "$tmp"' EXIT

# 정렬은 파일명 순 — 셸 글롭이 이미 그렇게 준다. 순서가 흔들리면 무의미한 diff 가 난다.
cat config/crd/bases/*.yaml > "$tmp"

if [[ "${1:-}" == "--check" ]]; then
	if diff -u "$bundle" "$tmp" > /dev/null; then
		echo "✓ chart-crds: 차트 CRD 가 생성물과 일치"
		exit 0
	fi
	echo "✗ 차트 CRD 가 config/crd/bases 와 어긋난다:" >&2
	diff -u "$bundle" "$tmp" | head -40 >&2
	echo "" >&2
	echo "make chart-crds 로 갱신한다. 손으로 맞추지 않는다 — 그래서 어긋난 것이다." >&2
	exit 1
fi

cp "$tmp" "$bundle"
echo "✓ chart-crds: $bundle 갱신 ($(grep -c '^  name: ' "$bundle") 개 CRD)"
