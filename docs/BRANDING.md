<p align="center">
  <b>English</b> |
  <a href="BRANDING.ko.md">한국어</a> |
  <a href="BRANDING.ja.md">日本語</a> |
  <a href="BRANDING.zh.md">中文</a>
</p>

# Branding Guide — `qdrant-operator`

> Visual identity, voice, and tone for `qdrant-operator`.

This document is the canonical reference for `qdrant-operator` branding decisions. It applies to the README, release notes, marketing material, and any third-party communication that represents the project.

## 1. Identity

**Organization**: [keiailab](https://keiailab.com) — Kubernetes-native data platform (license-clean, vanilla-upstream compatible).

**Project**: `qdrant-operator` — MIT-licensed Qdrant Operator for Kubernetes — declarative distributed vector-database clusters via the `QdrantCluster` CRD, vanilla-upstream Qdrant, license-clean.

> Note: `qdrant-operator` is **MIT** licensed, consistent with the sibling operators in the family — reference this project's actual `LICENSE` (MIT).

## 2. Logo & Visual Assets

| Asset | URL | Usage |
|---|---|---|
| Current primary logo | [`branding/symbol.png`](branding/symbol.png) | README header, slides, Artifact Hub package icon |
| Keiailab base symbol | [`branding/base-symbol.png`](branding/base-symbol.png) | Shared source reference for the outer rotating-arrow mark |
| Current favicon | `https://keiailab.com/favicon.ico` | Favicon, social cards |
| Light/dark wordmark | _planned_ | Not yet produced — must follow the 3-color ring grammar when added |
| Cover image | _planned_ | Deferred (family cover redesign track) |
| SVG kit | _planned_ | Future replacement after `https://keiailab.com/assets/{logo,mark,wordmark}.svg` return 200 |

**Logo placement**: Top-center of README, width 96px. Always link to https://keiailab.com.

**Clear space**: Minimum padding around logo = 25% of logo width.

**Do not**:
- Recolor the logo or the rotating-arrow ring
- Add drop shadows or filters to the ring
- Place on backgrounds with insufficient contrast
- Combine with other logos without keiailab brand approval

### 2.1 Symbol grammar (how `symbol.png` is built)

The family symbol is a **shared base ring + a per-project domain glyph** composited into its center. Preserve this grammar for any future re-render.

- **Base ring (invariant)**: `base-symbol.png` (460×460, shared byte-identical across the family, sha256 `f497a4db…`). The 3D clockwise rotating-arrow ring — dark-navy/slate (top-left) → blue (right) → green (bottom) — is **inviolable**: never recolor, redraw, filter, or shadow it. Only the central blue→green glossy sphere is masked out and replaced by the project glyph.
- **qdrant glyph — vector kNN constellation**: an asymmetric "nearest-neighbor" star that expresses Qdrant's domain truth (points in an embedding space + similarity links), deliberately distinct from the family's other glyphs (mongo leaf / valkey hexagon+pin / postgres elephant / commons 2×2 grid). It uses an **original motif** — Qdrant's official trademark is not reproduced.
  - **Anchor (query vector)**: one filled teal circle at center with a navy outline and a white top-left specular highlight (a micro-echo of the base sphere's gloss, tying the glyph to the family).
  - **Neighbors (3)**: asymmetric around the anchor at varied radii (embedding scatter). Two are white with navy outlines; **one is amber** — the nearest match.
  - **Edges (3)**: thin navy connectors from anchor to each neighbor, each ending in a small arrowhead that micro-echoes the ring's arrow motif (directional query → nearest neighbor).
- **Placement**: glyph lives strictly inside the ring's central clear-zone (canvas center 230,230), well clear of the ring's inner edge. Ring pixels are never touched (verified pixel-diff = 0 outside the clear-zone).
- **Downscale contract**: must stay legible (anchor + 3 neighbors + 3 edges, never overcrowded) at 96px (README) and 32px (Artifact Hub card / favicon).

## 3. Color Palette

| Role | Hex | Usage |
|---|---|---|
| Primary (keiailab teal) | `#0EA5A8` | Headers, primary actions, links, symbol anchor |
| Secondary (deep navy) | `#0F172A` | Dark backgrounds, code blocks, symbol outlines & edges |
| Accent (warm amber) | `#F59E0B` | Highlights, badge accents, symbol nearest-match neighbor |
| Neutral grey | `#64748B` | Body text on light backgrounds |
| Background light | `#F8FAFC` | Documentation page background |
| Background dark | `#020617` | Code editor theme, dark mode |

The symbol uses only palette hex values (teal / navy / amber / white, plus the ring's own green `#4FA84F`). GitHub README shield.io badges should use the hex values above.

## 4. Typography

- **Headings**: System default (GitHub's default `-apple-system, BlinkMacSystemFont, Segoe UI, ...`)
- **Body**: same (GitHub-native)
- **Code**: `ui-monospace, SFMono-Regular, Consolas, ...` (GitHub's default monospace)

No separate webfont (GitHub README rendering consistency).

## 5. Voice & Tone

**Audience**: Kubernetes platform engineers / vector-search & ML-platform teams / SRE.

**Voice principles**:
- **Direct** — bullet-point over paragraph where possible
- **Evidence-based** — claims include benchmark / SLA / link
- **Vendor-neutral** — reference upstream Qdrant but do not embed/wrap third-party operators
- **Honest about scope** — state the current Phase and its intentional limits (see README); do not imply unimplemented capability
- **License-aware** — MIT/BSD/Apache-2.0 dependencies only

**Avoid**:
- Marketing superlatives ("blazing fast", "revolutionary", "best-in-class")
- Vague comparisons ("X-class quality") — qualify with a specific metric or benchmark
- Time-based deadlines in roadmap (use a feature checklist instead)

## 6. README Header Standard

```markdown
<p align="center">
  <img src="docs/branding/symbol.png" alt="keiailab" width="96"/>
</p>

# qdrant-operator

> **MIT-licensed Qdrant Operator for Kubernetes — declarative distributed vector-database clusters via the `QdrantCluster` CRD**

<p align="center">
  <a href="LICENSE"><img src="https://img.shields.io/badge/License-MIT-blue.svg" alt="License"/></a>
  <!-- License SPDX = this repo's actual license (MIT). -->
</p>
```

## 7. README Footer Standard

```markdown
---

<p align="center">
  © 2026 keiailab · <a href="LICENSE">MIT</a> · <a href="https://keiailab.com">keiailab.com</a>
</p>
```

## 8. Badges — standard order

README shield.io badge order (left → right):

1. License (MIT)
2. Go Version
3. Vector Database (Qdrant)
4. Kubernetes Version (1.29+)
5. Container Image (ghcr.io/keiailab)
6. Helm Chart (Chart.yaml version + Artifact Hub link)
7. OpenSSF Scorecard
8. GitHub Discussions

## 9. Discussions / Issues / PR Templates

- **Discussions**: `https://github.com/keiailab/qdrant-operator/discussions` — feature ideas, Q&A
- **Issues**: bug reports + concrete feature requests with a use case
- **PR template**: `.github/PULL_REQUEST_TEMPLATE.md` (user scenario + verification command required)

## 10. Social & External

- **Website**: https://keiailab.com
- **GitHub Org**: https://github.com/keiailab
- **Artifact Hub** (Helm): https://artifacthub.io/packages/search?repo=keiailab-qdrant-operator
- **GHCR** (Container): https://github.com/keiailab/qdrant-operator/pkgs/container/qdrant-operator

## 11. License & Attribution

- License: [MIT](../LICENSE)
- Copyright: © 2026 keiailab contributors
- Third-party attributions: see `NOTICE` (if applicable)

---

<p align="center">
  © 2026 keiailab · <a href="../LICENSE">MIT</a> · <a href="https://keiailab.com">keiailab.com</a>
</p>
