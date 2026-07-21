#!/usr/bin/env bash
# OSS 발행 4채널 일치 검증 — GitHub 태그 / 컨테이너 이미지 / ghcr chart / 중앙 카탈로그.
#
# 배경: 라이브(Flux)는 GitHub main 을 직접 추적해 자동으로 최신이 되지만, 공개 배포
# 채널(chart)은 릴리스마다 사람이 발행해야 해서 조용히 뒤처진다(2026-07-21 실측:
# 라이브 v0.6.0 인데 ArtifactHub 0.4.0). 이 스크립트가 그 drift 를 결정론으로 잡는다.
#
# 사용: hack/verify-publish.sh [version]   (인자 없으면 Chart.yaml 의 version 사용)
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
chart_yaml="${repo_root}/deploy/chart/Chart.yaml"
chart_name="$(awk '/^name:/ {print $2; exit}' "$chart_yaml")"
version="${1:-$(awk '/^version:/ {print $2; exit}' "$chart_yaml")}"
app_version="$(awk '/^appVersion:/ {gsub(/"/,"",$2); print $2; exit}' "$chart_yaml")"

github_repo="${GITHUB_REPO:-KeiaiLab/${chart_name}}"
image_repo="${IMAGE_REPO:-registry.keiailab.com/keiailab/oss/${chart_name}}"
image_registry_host="${image_repo%%/*}"
ghcr_chart="${GHCR_CHART:-keiailab/charts/${chart_name}}"
catalog_index="${CATALOG_INDEX:-https://keiailab.github.io/charts/index.yaml}"

fail=0
ok()   { printf '  ✓ %s\n' "$1"; }
bad()  { printf '  ✗ %s\n' "$1"; fail=1; }

printf '발행 일관성 검증: %s chart=%s app=%s\n' "$chart_name" "$version" "$app_version"

# 0) chart version ↔ appVersion 정렬 (appVersion 은 v 접두 관례)
if [[ "v${version}" == "${app_version}" ]]; then
	ok "Chart.yaml: version(${version}) ↔ appVersion(${app_version}) 정렬"
else
	bad "Chart.yaml: version(${version}) 과 appVersion(${app_version}) 불일치 — appVersion 은 v<version> 이어야 함"
fi

# 1) GitHub 태그 존재 + 최신 여부
tags_json="$(curl -fsSL "https://api.github.com/repos/${github_repo}/tags?per_page=100" 2>/dev/null || echo '[]')"
latest_tag="$(printf '%s' "$tags_json" | python3 -c '
import json,sys,re
try: tags=[t["name"] for t in json.load(sys.stdin)]
except Exception: tags=[]
sem=[t for t in tags if re.fullmatch(r"v\d+\.\d+\.\d+", t)]
key=lambda t: tuple(int(x) for x in t[1:].split("."))
print(sorted(sem,key=key)[-1] if sem else "")')"
if printf '%s' "$tags_json" | grep -q "\"name\": *\"${app_version}\""; then
	ok "GitHub 태그 ${app_version} 존재"
else
	bad "GitHub 태그 ${app_version} 없음 (${github_repo})"
fi
if [[ -n "$latest_tag" && "$latest_tag" != "$app_version" ]]; then
	bad "GitHub 최신 태그는 ${latest_tag} — chart(${app_version})가 뒤처짐"
fi

# 2) 컨테이너 이미지 (익명 pull 토큰 경유 — 공개성까지 함께 검증)
#
# 토큰 발급처는 레지스트리 호스트가 아니라 인증 서버(GitLab)다 — 레지스트리가 401 과 함께
# 돌려주는 WWW-Authenticate 의 realm/service 를 그대로 따른다(호스트 하드코딩 회피).
auth_hdr="$(curl -sSI "https://${image_registry_host}/v2/${image_repo#*/}/manifests/${app_version}" 2>/dev/null |
	tr -d '\r' | awk 'tolower($1)=="www-authenticate:" {sub(/^[^ ]+ /,""); print; exit}')"
auth_realm="$(printf '%s' "$auth_hdr" | sed -n 's/.*realm="\([^"]*\)".*/\1/p')"
auth_service="$(printf '%s' "$auth_hdr" | sed -n 's/.*service="\([^"]*\)".*/\1/p')"
img_token=''
if [[ -n "$auth_realm" ]]; then
	img_token="$(curl -fsSL "${auth_realm}?service=${auth_service}&scope=repository:${image_repo#*/}:pull" 2>/dev/null |
		python3 -c 'import json,sys; print(json.load(sys.stdin).get("token",""))' 2>/dev/null || echo '')"
fi
img_code="$(curl -sS -o /dev/null -w '%{http_code}' \
	-H "Authorization: Bearer ${img_token}" \
	-H 'Accept: application/vnd.oci.image.index.v1+json,application/vnd.oci.image.manifest.v1+json,application/vnd.docker.distribution.manifest.v2+json' \
	"https://${image_registry_host}/v2/${image_repo#*/}/manifests/${app_version}" 2>/dev/null || echo 000)"
[[ "$img_code" == "200" ]] && ok "이미지 ${image_repo}:${app_version} 익명 pull 가능" \
	|| bad "이미지 ${image_repo}:${app_version} 익명 조회 실패(HTTP ${img_code}) — 미발행 또는 비공개"

# 3) ghcr chart (OCI, 익명)
ghcr_token="$(curl -fsSL "https://ghcr.io/token?scope=repository:${ghcr_chart}:pull" 2>/dev/null |
	python3 -c 'import json,sys; print(json.load(sys.stdin).get("token",""))' 2>/dev/null || echo '')"
ghcr_code="$(curl -sS -o /dev/null -w '%{http_code}' \
	-H "Authorization: Bearer ${ghcr_token}" \
	-H 'Accept: application/vnd.oci.image.manifest.v1+json' \
	"https://ghcr.io/v2/${ghcr_chart}/manifests/${version}" 2>/dev/null || echo 000)"
[[ "$ghcr_code" == "200" ]] && ok "chart oci://ghcr.io/${ghcr_chart}:${version} 익명 pull 가능" \
	|| bad "chart ghcr ${version} 익명 조회 실패(HTTP ${ghcr_code}) — helm push 누락 또는 패키지 비공개"

# 4) 중앙 카탈로그(ArtifactHub 가 크롤하는 index)
idx_version="$(curl -fsSL "$catalog_index" 2>/dev/null | python3 -c "
import sys,re
name='${chart_name}'
cur=None; out=''
for line in sys.stdin:
    m=re.match(r'^  ([a-z0-9-]+):\s*$', line)
    if m: cur=m.group(1); continue
    if cur==name:
        # chart 버전은 정확히 4칸 들여쓰기 — CRD 어노테이션 안의 'version: v1alpha1'
        # 을 잡지 않도록 레벨을 고정한다(mongodb/valkey index 실측).
        v=re.match(r'^    version:\s*(\S+)\s*$', line)
        if v: out=v.group(1); break
print(out)" 2>/dev/null || echo '')"
if [[ "$idx_version" == "$version" ]]; then
	ok "중앙 카탈로그 index 버전 ${idx_version} 일치"
else
	bad "중앙 카탈로그 index 버전 '${idx_version:-없음}' ≠ chart ${version} — catalog.yaml 승급 누락"
fi

if ((fail)); then
	printf '\n✗ 발행 일관성 위반 — 위 항목을 해소해야 릴리스가 완결된다(hack/release.sh 는 전 단계를 자동 수행).\n'
	exit 1
fi
printf '\n✓ 발행 4채널 일치 (GitHub / 이미지 / ghcr chart / 카탈로그)\n'
