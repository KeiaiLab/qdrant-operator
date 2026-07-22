<p align="center">
  <a href="BRANDING.md">English</a> |
  <a href="BRANDING.ko.md">한국어</a> |
  <b>日本語</b> |
  <a href="BRANDING.zh.md">中文</a>
</p>

# ブランディングガイド — `qdrant-operator` (日本語)

> 英語原文: [BRANDING.md](BRANDING.md) — canonical / 正本

> `qdrant-operator` の視覚的アイデンティティ、ボイス、トーン。

本ドキュメントは `qdrant-operator` のブランディング決定における正本(canonical)リファレンスです。README、リリースノート、マーケティング資料、そしてプロジェクトを代表するあらゆる third-party コミュニケーションに適用されます。

## 1. Identity

**組織**: [keiailab](https://keiailab.com) — Kubernetes-native data platform(license-clean、vanilla-upstream 互換)。

**プロジェクト**: `qdrant-operator` — 単一の `QdrantCluster` CRD を通じて宣言的な分散ベクトルデータベースクラスターを提供する MIT ライセンスの Qdrant Operator — vanilla-upstream Qdrant、license-clean。

> 注記: `qdrant-operator` はファミリー内の兄弟オペレーターと同じく **MIT** ライセンスです — 本プロジェクトの実際の `LICENSE`(MIT)を参照してください。

## 2. ロゴ & Visual Assets

| アセット | URL | 用途 |
|---|---|---|
| 現在のプライマリロゴ | [`branding/symbol.png`](branding/symbol.png) | README ヘッダー、スライド、Artifact Hub パッケージアイコン |
| Keiailab ベースシンボル | [`branding/base-symbol.png`](branding/base-symbol.png) | 外周の回転矢印マークの共有ソースリファレンス |
| 現在のファビコン | `https://keiailab.com/favicon.ico` | ファビコン、ソーシャルカード |
| Light/dark ワードマーク | _計画中_ | 未制作 — 追加時は 3 色リンググラマーに従うこと |
| カバー画像 | _計画中_ | 保留(ファミリーのカバー再デザイントラック) |
| SVG キット | _計画中_ | `https://keiailab.com/assets/{logo,mark,wordmark}.svg` が 200 を返すようになった後の将来の置き換え |

**ロゴ配置**: README 上部中央、width 96px。常に https://keiailab.com へリンクします。

**Clear space**: ロゴ周囲の最小 padding = ロゴ幅の 25%。

**禁止事項**:
- ロゴまたは回転矢印リングの再カラーリング
- リングへの drop shadow やフィルターの追加
- コントラストが不十分な背景への配置
- keiailab ブランドの承認なしに他のロゴと組み合わせること

### 2.1 シンボルグラマー(`symbol.png` の構成方法)

ファミリーシンボルは、**共有のベースリング + プロジェクトごとのドメイングリフ** を中心に合成したものです。将来 re-render する際も、このグラマーを維持してください。

- **ベースリング(不変)**: `base-symbol.png`(460×460、ファミリー全体でバイト単位まで同一、sha256 `f497a4db…`)。時計回りに回転する 3D 矢印リング — ダークネイビー/スレート(左上)→ ブルー(右)→ グリーン(下)— は **不可侵** です。再カラーリング・再描画・フィルター適用・シャドウ付与は一切禁止です。中央のブルー→グリーンの光沢球体のみがマスクされ、プロジェクト固有のグリフに置き換えられます。
- **qdrant グリフ — ベクトル kNN コンステレーション**: Qdrant のドメインの本質(embedding 空間内の点 + 類似度リンク)を表現する非対称な「最近傍」スター型グリフで、ファミリー内の他プロジェクトのグリフ(mongo のリーフ / valkey の六角形+ピン / postgres の象 / commons の 2×2 グリッド)とは意図的に区別されています。**オリジナルのモチーフ** を採用しており、Qdrant の公式商標は再現していません。
  - **アンカー(クエリベクトル)**: 中心に配置された、ネイビーの輪郭線を持つ塗りつぶしのティール色の円で、左上に白いスペキュラーハイライトが入ります(ベースの球体の光沢を微かに反復し、グリフをファミリーへつなぎます)。
  - **近傍点(3 個)**: アンカーの周囲に、半径を変えて非対称に配置されます(embedding の散らばりを表現)。2 個は白地にネイビーの輪郭線、**残り 1 個はアンバー色** — 最も近いマッチを示します。
  - **エッジ(3 本)**: アンカーから各近傍点へ伸びる細いネイビーの連結線です。それぞれの先端には小さな矢じりが付いており、リングの矢印モチーフを微かに反映しています(方向を持つクエリ → 最近傍、を表現)。
- **配置**: グリフはリング中央のクリアゾーン内(キャンバス中心 230,230)に厳密に収まり、リング内側の縁からは十分な余白を保ちます。リングのピクセルには一切手を加えません(クリアゾーン外の pixel-diff = 0 を検証済み)。
- **縮小時の契約**: 96px(README)および 32px(Artifact Hub カード / favicon)においても、アンカー + 近傍点 3 個 + エッジ 3 本が判読可能であり、決して過密にならないことを保証します。

## 3. カラーパレット

| Role | Hex | 用途 |
|---|---|---|
| Primary (keiailab teal) | `#0EA5A8` | ヘッダー、primary アクション、リンク、シンボルのアンカー |
| Secondary (deep navy) | `#0F172A` | 暗い背景、コードブロック、シンボルのアウトライン & エッジ |
| Accent (warm amber) | `#F59E0B` | ハイライト、バッジ accent、シンボルの nearest-match 近傍点 |
| Neutral grey | `#64748B` | 明るい背景上の本文テキスト |
| Background light | `#F8FAFC` | ドキュメントページ背景 |
| Background dark | `#020617` | コードエディタテーマ、dark mode |

シンボルはパレットの hex 値のみを使用します(teal / navy / amber / white、およびリング固有の green `#4FA84F`)。GitHub README の shield.io バッジは上記 hex 値を使用してください。

## 4. タイポグラフィ

- **Headings**: System default(GitHub の default `-apple-system, BlinkMacSystemFont, Segoe UI, ...`)
- **Body**: 同上(GitHub-native)
- **Code**: `ui-monospace, SFMono-Regular, Consolas, ...`(GitHub の default monospace)

別途 webfont は使用しません(GitHub README レンダリングとの整合性のため)。

## 5. Voice & Tone

**読者**: Kubernetes プラットフォームエンジニア / ベクトル検索 & ML プラットフォームチーム / SRE。

**Voice 原則**:
- **Direct(直接的)** — 可能な限り段落より bullet-point
- **Evidence-based(根拠ベース)** — 主張には benchmark / SLA / リンクを含める
- **Vendor-neutral(ベンダーニュートラル)** — upstream Qdrant を参照しつつ third-party operator を embed/wrap しない
- **スコープに正直であること** — 現在の Phase とその意図的な制限を明示する(README 参照);未実装の機能を示唆しない
- **License-aware(ライセンス意識)** — MIT/BSD/Apache-2.0 依存関係のみ

**避けるべき表現**:
- マーケティング的な最上級表現(“blazing fast”、“revolutionary”、“best-in-class”)
- 曖昧な比較(“X-class quality”)— 具体的なメトリクスまたは benchmark で裏付けること
- ロードマップにおける時間ベースの締切(代わりに feature checklist を使用)

## 6. README ヘッダー標準

```markdown
<p align="center">
  <img src="docs/branding/symbol.png" alt="keiailab" width="96"/>
</p>

# qdrant-operator

> **MIT-licensed Qdrant Operator for Kubernetes — declarative distributed vector-database clusters via the `QdrantCluster` CRD**

<p align="center">
  <a href="LICENSE"><img src="https://img.shields.io/badge/License-MIT-blue.svg" alt="License"/></a>
  <!-- License SPDX = 本リポジトリの実際のライセンス(MIT)。 -->
</p>
```

## 7. README フッター標準

```markdown
---

<p align="center">
  © 2026 keiailab · <a href="LICENSE">MIT</a> · <a href="https://keiailab.com">keiailab.com</a>
</p>
```

## 8. Badges — 標準順序

README の shield.io バッジ順序(左→右):

1. License (MIT)
2. Go Version
3. Vector Database (Qdrant)
4. Kubernetes Version (1.29+)
5. Container Image (ghcr.io/keiailab)
6. Helm Chart (Chart.yaml version + Artifact Hub link)
7. OpenSSF Scorecard
8. GitHub Discussions

## 9. Discussions / Issues / PR Templates

- **Discussions**: `https://github.com/keiailab/qdrant-operator/discussions` — 機能アイデア、Q&A
- **Issues**: バグ報告 + ユースケースを伴う具体的な機能要望
- **PR template**: `.github/PULL_REQUEST_TEMPLATE.md`(ユーザーシナリオ + 検証コマンドの記載が必須)

## 10. Social & External

- **Website**: https://keiailab.com
- **GitHub Org**: https://github.com/keiailab
- **Artifact Hub** (Helm): https://artifacthub.io/packages/search?repo=keiailab-qdrant-operator
- **GHCR** (Container): https://github.com/keiailab/qdrant-operator/pkgs/container/qdrant-operator

## 11. License & Attribution

- License: [MIT](../LICENSE)
- Copyright: © 2026 keiailab contributors
- Third-party attributions: 該当する場合は `NOTICE` を参照

---

<p align="center">
  © 2026 keiailab · <a href="../LICENSE">MIT</a> · <a href="https://keiailab.com">keiailab.com</a>
</p>
