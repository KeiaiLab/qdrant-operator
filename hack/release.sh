#!/usr/bin/env bash
# OSS 릴리스 단일 진입점 — 게이트부터 5채널 발행까지 한 번에.
#
# 왜 단일 명령인가: 채널이 5개(GitHub 태그 / 컨테이너 이미지(registry.keiailab.com) /
# ghcr 이미지(values.yaml image.repository 기본값) / ghcr chart / 중앙 카탈로그)
# 라 수동 절차는 반드시 하나를 빠뜨린다(2026-07-21 실측: 라이브 v0.6.0 인데 공개 chart
# 0.4.0 — 2버전 잠복). 절차를 사람이 기억하지 않도록 코드에 고정한다.
#
# 사용: hack/release.sh 0.7.0        (버전은 chart semver — 이미지/태그는 v 접두)
#       DRY_RUN=1 hack/release.sh 0.7.0   (발행 없이 단계만 출력)
set -euo pipefail

version="${1:-}"
[[ -n "$version" ]] || { echo "사용: $0 <version>  (예: 0.7.0)" >&2; exit 1; }
[[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || { echo "version 은 semver(x.y.z) — 'v' 없이" >&2; exit 1; }

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"
chart_yaml="deploy/chart/Chart.yaml"
chart_name="$(awk '/^name:/ {print $2; exit}' "$chart_yaml")"
tag="v${version}"
image_repo="${IMAGE_REPO:-registry.keiailab.com/keiailab/oss/${chart_name}}"
# ghcr 이미지 채널 — deploy/chart/values.yaml image.repository 기본값과 정합해야
# image.tag 미지정 소비자(라이브 Flux 포함)가 실제로 pull 가능해진다.
ghcr_image_repo="${GHCR_IMAGE_REPO:-ghcr.io/keiailab/${chart_name}}"
ghcr_repo="${GHCR_REPO:-oci://ghcr.io/keiailab/charts}"
charts_repo_dir="${CHARTS_REPO_DIR:-${repo_root}/../charts}"
gitlab_mirror_project="${GITLAB_MIRROR_PROJECT:-294}" # archived 미러 — push 창에만 unarchive
dry="${DRY_RUN:-0}"

run() { if ((dry)); then printf '  [dry] %s\n' "$*"; else "$@"; fi; }
step() { printf '\n▶ %s\n' "$1"; }

step "0/7 사전 확인 (clean tree · 태그 미중복)"
[[ -z "$(git status --porcelain)" ]] || { echo "워킹트리가 dirty — 커밋/정리 후 재실행" >&2; exit 1; }
git fetch -q --tags origin
! git rev-parse -q --verify "refs/tags/${tag}" >/dev/null || { echo "태그 ${tag} 가 이미 존재" >&2; exit 1; }

step "1/7 품질 게이트 (test · lint · publish-scan)"
run make test
run make lint
run make publish-scan

step "2/7 Chart.yaml 버전 정렬 (${version} / ${tag})"
run sed -i '' -E "s/^version: .*/version: ${version}/; s/^appVersion: .*/appVersion: \"${tag}\"/" "$chart_yaml"
run helm lint deploy/chart
if [[ -n "$(git status --porcelain "$chart_yaml")" ]]; then
	run git add "$chart_yaml"
	run git commit -m "chore(chart): ${version} 릴리스 버전 정렬"
fi

step "3/7 GitHub main push + 태그"
run git push origin main
run git tag "$tag"
run git push origin "$tag"

step "4/7 컨테이너 이미지 (linux/amd64 단일 · default 빌더)"
run docker --context=default buildx build --builder default --platform linux/amd64 -t "${image_repo}:${tag}" --load .
run glab api --method POST "projects/${gitlab_mirror_project}/unarchive"
run docker push "${image_repo}:${tag}"
run glab api --method POST "projects/${gitlab_mirror_project}/archive"
# ghcr 이미지 채널(values.yaml image.repository 기본값) — glab archive 창 밖에서
# 태그·푸시해 GitLab unarchive 창을 최소화한다. 자격증명은 step5(helm push
# oci://ghcr.io/…)와 동일한 `docker login ghcr.io`(write:packages) 전제 — 신규 인증경로 아님.
run docker tag "${image_repo}:${tag}" "${ghcr_image_repo}:${tag}"
run docker push "${ghcr_image_repo}:${tag}"

step "5/7 helm chart → ghcr OCI"
pkg_dir="$(mktemp -d)"
run helm package deploy/chart --destination "$pkg_dir"
run helm push "${pkg_dir}/${chart_name}-${version}.tgz" "$ghcr_repo"
rm -rf "$pkg_dir"

step "6/7 중앙 카탈로그 승급 (ArtifactHub 크롤 대상)"
if [[ -d "$charts_repo_dir/.git" ]]; then
	run git -C "$charts_repo_dir" pull -q
	run python3 -c "
import re,sys
p='${charts_repo_dir}/catalog.yaml'
s=open(p).read()
pat=re.compile(r'(- name: ${chart_name}\n    version: )\S+')
if not pat.search(s):
    s=s.rstrip()+'\n  - name: ${chart_name}\n    version: ${version}\n'
else:
    s=pat.sub(r'\g<1>${version}', s)
open(p,'w').write(s)
print('catalog.yaml → ${chart_name} ${version}')"
	run bash "$charts_repo_dir/hack/update-index.sh"
	run git -C "$charts_repo_dir" add catalog.yaml index.yaml
	run git -C "$charts_repo_dir" commit -m "chore(catalog): ${chart_name} ${version} 승급"
	run git -C "$charts_repo_dir" push origin main
else
	echo "  ! 중앙 카탈로그 repo 없음(${charts_repo_dir}) — CHARTS_REPO_DIR 지정 후 재실행 필요" >&2
	((dry)) || exit 1
fi

step "7/7 발행 일관성 검증 (5채널)"
if ((dry)); then
	printf '  [dry] hack/verify-publish.sh %s\n' "$version"
else
	# ghcr/카탈로그 전파에 수 초 걸릴 수 있어 짧게 재시도한다.
	for i in 1 2 3 4 5; do
		if bash hack/verify-publish.sh "$version"; then
			printf '\n✓ 릴리스 %s 완결 — 5채널 일치\n' "$tag"
			exit 0
		fi
		printf '  … 전파 대기 재시도 %d/5\n' "$i"
		sleep 20
	done
	printf '\n✗ 릴리스 %s 는 발행됐으나 일관성 검증 실패 — 위 항목 확인\n' "$tag" >&2
	exit 1
fi
