#!/usr/bin/env bash
# 공개 적합성 스캔 — push 전 이 저장소의 추적 파일에 조직 내부 참조/타 프로젝트 평가성
# 표현이 섞이지 않았는지 검사한다. 이 저장소는 공개 OSS 이므로, 내부 거버넌스 문서
# 식별자·조직 운영 이력·타 프로젝트에 대한 평가는 커밋 대상이 아니다.
#
# 사용: make publish-scan  (push 전 의무 게이트)
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"

# 위험 패턴 (대소문자 무시). 필요 시 추가.
PATTERNS=(
  'RFC-0[0-9]{3}'   # 내부 RFC 식별자
  'GOVERNANCE'      # 내부 거버넌스 문서 참조
  '사내'
  '사망'             # 타 프로젝트 평가성 표현
  'billing'         # 조직 운영/과금 이력
  'SPOF'
  'incident'
)
# 예외 라인 allowlist. LICENSE 는 표준 법률 텍스트(incidental 등)라 파일 단위 제외.
# 커뮤니티 헬스 문서(.github/)는 공개 OSS 표준 문서라 해당 단어의 정당 사용 라인만
# 정밀 제외한다 — 파일 단위 제외는 내부 참조 유출 검출력을 깎으므로 금지.
ALLOW=(
  'publish-scan'    # 본 스크립트/Makefile 의 자기 언급
  '^LICENSE:'       # MIT 표준 문구 (incidental damages 등)
  '^\.github/GOVERNANCE\.md:[0-9]*:.*[Gg]overnance'  # 공개 표준 거버넌스 문서의 자기 언급(형제 패밀리 동일 보유)
  '^\.github/SUPPORT\.md:[0-9]*:.*GOVERNANCE\.md'    # 표준 문서로의 상호 링크
  '^\.github/CODEOWNERS:[0-9]*:.*[Gg]overnance'      # 소유 구획 주석
  '^\.github/CODE_OF_CONDUCT\.md:[0-9]*:.*incident'  # Contributor Covenant v2.1 표준 문구
)

fail=0
for pat in "${PATTERNS[@]}"; do
  hits=$(git ls-files -z | xargs -0 grep -InEi "$pat" 2>/dev/null || true)
  # allowlist 라인 제거
  for a in "${ALLOW[@]}"; do
    hits=$(printf '%s\n' "$hits" | grep -v "$a" || true)
  done
  if [ -n "$hits" ]; then
    echo "✗ 공개 부적합 패턴 '$pat' 발견:"
    printf '%s\n' "$hits" | head -10
    fail=1
  fi
done

if [ "$fail" -ne 0 ]; then
  echo "→ 위 항목을 제거/중립화하기 전에는 push 금지 (공개 저장소)."
  exit 1
fi
echo "✓ publish-scan: 공개 부적합 패턴 0건"
