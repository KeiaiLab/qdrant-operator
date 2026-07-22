<p align="center">
  <a href="BRANDING.md">English</a> |
  <b>한국어</b> |
  <a href="BRANDING.ja.md">日本語</a> |
  <a href="BRANDING.zh.md">中文</a>
</p>

# Branding 가이드 — `qdrant-operator` (한국어)

> 영문 원본: [BRANDING.md](BRANDING.md) — canonical / 정본

> `qdrant-operator` 의 시각 identity, voice, tone.

본 문서는 `qdrant-operator` 브랜딩 결정에 대한 정본(canonical) 참조다. README, 릴리스 노트, 마케팅 자료, 그리고 프로젝트를 대표하는 모든 third-party 커뮤니케이션에 적용된다.

## 1. Identity

**조직**: [keiailab](https://keiailab.com) — Kubernetes-native data platform (license-clean, vanilla-upstream 호환).

**프로젝트**: `qdrant-operator` — 단일 `QdrantCluster` CRD를 통해 선언적 분산 벡터 데이터베이스 클러스터를 제공하는 MIT 라이선스 Qdrant Operator — vanilla-upstream Qdrant, license-clean.

> 참고: `qdrant-operator`는 패밀리의 형제 오퍼레이터들과 마찬가지로 **MIT** 라이선스다 — 본 프로젝트의 실제 `LICENSE`(MIT)를 참조하라.

## 2. 로고 & Visual Assets

| 자산 | URL | 용도 |
|---|---|---|
| 현재 기본 로고 | [`branding/symbol.png`](branding/symbol.png) | README 헤더, 슬라이드, Artifact Hub 패키지 아이콘 |
| Keiailab 베이스 심볼 | [`branding/base-symbol.png`](branding/base-symbol.png) | 바깥쪽 회전 화살표 마크의 공유 소스 레퍼런스 |
| 현재 파비콘 | `https://keiailab.com/favicon.ico` | 파비콘, 소셜 카드 |
| Light/dark 워드마크 | _예정_ | 아직 제작되지 않음 — 추가 시 3색 링 그래머를 따라야 함 |
| 커버 이미지 | _예정_ | 보류 (패밀리 커버 재디자인 트랙) |
| SVG 킷 | _예정_ | `https://keiailab.com/assets/{logo,mark,wordmark}.svg` 가 200을 반환한 이후의 향후 교체본 |

**로고 배치**: README 상단 중앙, width 96px. 항상 https://keiailab.com 으로 링크.

**Clear space**: 로고 둘레 최소 padding = 로고 너비의 25%.

**금지 사항**:
- 로고 또는 회전 화살표 링의 색상 변경
- 링에 drop shadow 또는 필터 추가
- 대비가 부족한 배경 위에 배치
- keiailab 브랜드 승인 없이 다른 로고와 결합

### 2.1 심볼 그래머 (`symbol.png` 가 만들어지는 방식)

패밀리 심볼은 **공유 베이스 링 + 프로젝트별 도메인 글리프**를 중앙에 합성한 것이다. 향후 재렌더링 시에도 이 그래머를 보존해야 한다.

- **베이스 링(불변)**: `base-symbol.png`(460×460, 패밀리 전체에서 byte-identical, sha256 `f497a4db…`). 시계 방향으로 회전하는 3D 화살표 링 — 다크 네이비/슬레이트(좌상단) → 블루(우측) → 그린(하단) — 은 **불가침**이다: 절대 재컬러링·재드로잉·필터링·섀도잉하지 않는다. 중앙의 블루→그린 글로시 스피어만 마스킹되어 프로젝트 글리프로 교체된다.
- **qdrant 글리프 — 벡터 kNN 콘스텔레이션**: Qdrant의 도메인 본질(embedding 공간의 점 + 유사도 링크)을 표현하는 비대칭 "최근접 이웃" 스타 형태로, 패밀리의 다른 글리프들(mongo의 리프 / valkey의 육각형+핀 / postgres의 코끼리 / commons의 2×2 그리드)과 의도적으로 구별된다. **오리지널 모티프**를 사용하며, Qdrant의 공식 상표는 재현하지 않는다.
  - **앵커(쿼리 벡터)**: 중앙에 위치한 채워진 teal 색 원으로, 네이비 아웃라인과 좌상단의 흰색 스펙큘러 하이라이트를 가진다(베이스 스피어의 광택을 미세하게 반향하여 글리프를 패밀리에 연결).
  - **이웃(3개)**: 앵커 주위에 서로 다른 반경으로 비대칭 배치된다(embedding scatter 표현). 2개는 흰색에 네이비 아웃라인이고, **1개는 amber 색**이다 — 가장 가까운 매치를 뜻한다.
  - **엣지(3개)**: 앵커에서 각 이웃으로 이어지는 얇은 네이비 커넥터로, 각각 링의 화살표 모티프를 미세하게 반향하는 작은 화살촉으로 끝난다(방향성 있는 쿼리 → 최근접 이웃을 표현).
- **배치**: 글리프는 링 중앙의 clear-zone 내부(캔버스 중심 230,230)에 엄격히 위치하며, 링 안쪽 가장자리로부터 충분히 떨어져 있다. 링 픽셀은 절대 건드리지 않는다(clear-zone 바깥 pixel-diff = 0 검증됨).
- **축소 계약**: 96px(README)와 32px(Artifact Hub 카드 / 파비콘)에서도 판독 가능해야 한다(앵커 + 이웃 3개 + 엣지 3개, 절대 과밀하지 않게).

## 3. Color Palette

| 역할 | Hex | 용도 |
|---|---|---|
| Primary (keiailab teal) | `#0EA5A8` | 헤더, primary action, 링크, 심볼 앵커 |
| Secondary (deep navy) | `#0F172A` | 어두운 배경, 코드 블록, 심볼 아웃라인 & 엣지 |
| Accent (warm amber) | `#F59E0B` | 강조, 배지 accent, 심볼의 nearest-match 이웃 |
| Neutral grey | `#64748B` | 밝은 배경의 본문 텍스트 |
| Background light | `#F8FAFC` | 문서 페이지 배경 |
| Background dark | `#020617` | 코드 에디터 테마, dark mode |

심볼은 팔레트 hex 값만 사용한다(teal / navy / amber / white, 그리고 링 고유의 green `#4FA84F`). GitHub README shield.io 배지는 위 hex 값을 사용해야 한다.

## 4. Typography

- **Headings**: System default (GitHub 의 default `-apple-system, BlinkMacSystemFont, Segoe UI, ...`)
- **Body**: 동일 (GitHub-native)
- **Code**: `ui-monospace, SFMono-Regular, Consolas, ...` (GitHub 의 default monospace)

별도 webfont 없음 (GitHub README 렌더링 정합).

## 5. Voice & Tone

**대상**: Kubernetes 플랫폼 엔지니어 / 벡터 검색 & ML 플랫폼 팀 / SRE.

**Voice 원칙**:
- **Direct** — 가능하면 문단보다 bullet-point
- **Evidence-based** — claim 은 벤치마크 / SLA / 링크를 포함
- **Vendor-neutral** — upstream Qdrant 를 참조하되 third-party operator 를 embed/wrap 하지 않음
- **스코프에 정직할 것** — 현재 Phase 와 그 의도적 한계를 명시(README 참고); 미구현 기능을 암시하지 않음
- **License-aware** — MIT/BSD/Apache-2.0 의존성만

**피할 것**:
- 마케팅 최상급 표현("blazing fast", "revolutionary", "best-in-class")
- 모호한 비교("X-class quality") — 구체적 메트릭 또는 벤치마크로 qualify
- 로드맵의 시간 기반 데드라인(대신 feature checklist 사용)

## 6. README Header Standard

```markdown
<p align="center">
  <img src="docs/branding/symbol.png" alt="keiailab" width="96"/>
</p>

# qdrant-operator

> **MIT-licensed Qdrant Operator for Kubernetes — declarative distributed vector-database clusters via the `QdrantCluster` CRD**

<p align="center">
  <a href="LICENSE"><img src="https://img.shields.io/badge/License-MIT-blue.svg" alt="License"/></a>
  <!-- License SPDX = 본 저장소의 실제 라이선스(MIT). -->
</p>
```

## 7. README Footer Standard

```markdown
---

<p align="center">
  © 2026 keiailab · <a href="LICENSE">MIT</a> · <a href="https://keiailab.com">keiailab.com</a>
</p>
```

## 8. Badges — 표준 순서

README shield.io 배지 순서(좌 → 우):

1. License (MIT)
2. Go Version
3. Vector Database (Qdrant)
4. Kubernetes Version (1.29+)
5. Container Image (ghcr.io/keiailab)
6. Helm Chart (Chart.yaml version + Artifact Hub link)
7. OpenSSF Scorecard
8. GitHub Discussions

## 9. Discussions / Issues / PR Templates

- **Discussions**: `https://github.com/keiailab/qdrant-operator/discussions` — 기능 아이디어, Q&A
- **Issues**: 버그 리포트 + 유스 케이스가 포함된 구체적 기능 요청
- **PR template**: `.github/PULL_REQUEST_TEMPLATE.md`(사용자 시나리오 + 검증 명령 필수)

## 10. Social & External

- **Website**: https://keiailab.com
- **GitHub Org**: https://github.com/keiailab
- **Artifact Hub** (Helm): https://artifacthub.io/packages/search?repo=keiailab-qdrant-operator
- **GHCR** (Container): https://github.com/keiailab/qdrant-operator/pkgs/container/qdrant-operator

## 11. License & Attribution

- License: [MIT](../LICENSE)
- Copyright: © 2026 keiailab contributors
- Third-party attributions: `NOTICE` 참조 (해당하는 경우)

---

<p align="center">
  © 2026 keiailab · <a href="../LICENSE">MIT</a> · <a href="https://keiailab.com">keiailab.com</a>
</p>
