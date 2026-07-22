<p align="center">
  <a href="BRANDING.md">English</a> |
  <a href="BRANDING.ko.md">한국어</a> |
  <a href="BRANDING.ja.md">日本語</a> |
  <b>中文</b>
</p>

# 品牌指南 — `qdrant-operator` (中文)

> 英文原文: [BRANDING.md](BRANDING.md) — canonical / 正本

> `qdrant-operator` 的视觉识别、voice 与 tone。

本文档是 `qdrant-operator` 品牌决策的正本(canonical)参考。适用于 README、发布说明、市场材料,以及代表本项目的所有第三方沟通。

## 1. Identity(身份定位)

**组织**: [keiailab](https://keiailab.com) —— Kubernetes-native 数据平台(license-clean,与 vanilla-upstream 兼容)。

**项目**: `qdrant-operator` —— 通过单个 `QdrantCluster` CRD 提供声明式分布式向量数据库集群的 MIT 许可 Qdrant Operator —— vanilla-upstream Qdrant,license-clean。

> 说明: `qdrant-operator` 与系列中的其他 operator 一样采用 **MIT** 许可证 —— 请以本项目实际的 `LICENSE` 文件(MIT)为准。

## 2. Logo 与视觉资产

| 资产 | URL | 用途 |
|---|---|---|
| 当前主 logo | [`branding/symbol.png`](branding/symbol.png) | README header、幻灯片、Artifact Hub 包图标 |
| Keiailab 基础符号 | [`branding/base-symbol.png`](branding/base-symbol.png) | 外圈旋转箭头标记的共享源参考 |
| 当前 favicon | `https://keiailab.com/favicon.ico` | favicon、社交卡片 |
| 亮色/暗色 wordmark | _计划中_ | 尚未制作 —— 后续新增时须遵循三色圆环语法 |
| Cover image | _计划中_ | 推迟(系列封面重新设计计划) |
| SVG kit | _计划中_ | 待 `https://keiailab.com/assets/{logo,mark,wordmark}.svg` 返回 200 后替换 |

**Logo placement**: README 顶部居中,宽度 96px。始终链接到 https://keiailab.com。

**Clear space**: logo 周围最小留白 = logo 宽度的 25%。

**禁止事项**:
- 更改 logo 或旋转箭头圆环的颜色
- 为圆环添加投影或滤镜
- 放置于对比度不足的背景上
- 未经 keiailab 品牌批准与其他 logo 组合

### 2.1 符号语法(`symbol.png` 的构成方式)

该系列符号由 **共享的基础圆环 + 各项目专属的领域图形(glyph)** 合成于圆环中心而成。未来任何重新渲染都必须保持这一语法规则。

- **基础圆环(不变)**: `base-symbol.png`(460×460,整个系列逐字节完全一致,sha256 为 `f497a4db…`)。这个顺时针旋转的 3D 箭头圆环 —— 深藏青/石板灰(左上)→ 蓝色(右)→ 绿色(下)—— **不可侵犯**:严禁重新着色、重新绘制、添加滤镜或阴影。只有中央蓝→绿的光泽球体会被遮罩并替换为各项目自己的 glyph。
- **qdrant glyph —— 向量 kNN 星座图**: 一个非对称的“最近邻”星形图案,用以表达 Qdrant 的领域本质(embedding 空间中的点 + 相似度连接),与系列中其他项目的 glyph(mongo 的叶子 / valkey 的六边形+图钉 / postgres 的大象 / commons 的 2×2 网格)刻意区分开来。它采用**原创图案** —— 并未复刻 Qdrant 的官方商标。
  - **锚点(查询向量)**: 中心处一个带藏青色描边的实心青绿色圆点,左上角带有白色高光(呼应基础球体的光泽,使 glyph 与整个系列保持视觉关联)。
  - **邻居点(3 个)**: 以不同半径非对称分布在锚点周围(表现 embedding 的散布)。其中两个为白色、藏青色描边;**一个为琥珀色** —— 代表最近的匹配项。
  - **连接线(3 条)**: 从锚点到各邻居点的细藏青色连线,每条线末端都带有一个小箭头,呼应圆环本身的箭头图案(表现有方向性的“查询 → 最近邻”)。
- **放置规则**: glyph 严格限定在圆环中央的留白区(canvas 中心 230,230)内,与圆环内边缘保持充分距离。圆环像素永远不会被触碰(已验证留白区之外的 pixel-diff = 0)。
- **缩小规则**: 在 96px(README)与 32px(Artifact Hub 卡片 / favicon)尺寸下,依然必须保持可辨识(锚点 + 3 个邻居点 + 3 条连接线,绝不可显得拥挤)。

## 3. 配色方案

| 角色 | Hex | 用途 |
|---|---|---|
| Primary (keiailab teal) | `#0EA5A8` | 标题、主要操作、链接、符号锚点 |
| Secondary (deep navy) | `#0F172A` | 深色背景、代码块、符号描边与连接线 |
| Accent (warm amber) | `#F59E0B` | 高亮、徽章 accent、符号中 nearest-match 邻居点 |
| Neutral grey | `#64748B` | 浅色背景下的正文文字 |
| Background light | `#F8FAFC` | 文档页面背景 |
| Background dark | `#020617` | 代码编辑器主题、dark mode |

符号仅使用调色板中的 hex 值(teal / navy / amber / white,以及圆环自身的 green `#4FA84F`)。GitHub README 的 shield.io 徽章应使用上述 hex 值。

## 4. 字体排印

- **Headings**: System default(GitHub 默认的 `-apple-system, BlinkMacSystemFont, Segoe UI, ...`)
- **Body**: 同上(与 GitHub 原生一致)
- **Code**: `ui-monospace, SFMono-Regular, Consolas, ...`(GitHub 默认 monospace)

不使用额外的 webfont(以保持与 GitHub README 渲染一致)。

## 5. Voice & Tone(声音与语调)

**受众**: Kubernetes 平台工程师 / 向量检索与 ML 平台团队 / SRE。

**Voice 原则**:
- **Direct(直接)** —— 尽可能用 bullet-point 而非大段文字
- **Evidence-based(基于证据)** —— 论断需附带 benchmark / SLA / 链接
- **Vendor-neutral(厂商中立)** —— 引用 upstream Qdrant,但不 embed / wrap 第三方 operator
- **对范围保持诚实** —— 明确当前 Phase 及其有意为之的局限(见 README);不得暗示未实现的能力
- **License-aware(许可证意识)** —— 仅使用 MIT/BSD/Apache-2.0 依赖

**应避免**:
- 营销式最高级表述(“blazing fast”、“revolutionary”、“best-in-class”)
- 模糊的比较(“X-class quality”)—— 应以具体指标或 benchmark 加以限定
- 路线图中的时间性截止日期(改用 feature checklist)

## 6. README Header 标准

```markdown
<p align="center">
  <img src="docs/branding/symbol.png" alt="keiailab" width="96"/>
</p>

# qdrant-operator

> **MIT-licensed Qdrant Operator for Kubernetes — declarative distributed vector-database clusters via the `QdrantCluster` CRD**

<p align="center">
  <a href="LICENSE"><img src="https://img.shields.io/badge/License-MIT-blue.svg" alt="License"/></a>
  <!-- License SPDX = 本仓库实际使用的许可证(MIT)。 -->
</p>
```

## 7. README Footer 标准

```markdown
---

<p align="center">
  © 2026 keiailab · <a href="LICENSE">MIT</a> · <a href="https://keiailab.com">keiailab.com</a>
</p>
```

## 8. Badges 标准顺序

README 中 shield.io 徽章的排列顺序(左→右):

1. License (MIT)
2. Go Version
3. Vector Database (Qdrant)
4. Kubernetes Version (1.29+)
5. Container Image (ghcr.io/keiailab)
6. Helm Chart (Chart.yaml version + Artifact Hub link)
7. OpenSSF Scorecard
8. GitHub Discussions

## 9. Discussions / Issues / PR Templates

- **Discussions**: `https://github.com/keiailab/qdrant-operator/discussions` —— 功能创意、Q&A
- **Issues**: bug 报告 + 附带用例的具体功能请求
- **PR template**: `.github/PULL_REQUEST_TEMPLATE.md`(必须包含用户场景 + 验证命令)

## 10. Social & External(社交与外部链接)

- **网站**: https://keiailab.com
- **GitHub Org**: https://github.com/keiailab
- **Artifact Hub** (Helm): https://artifacthub.io/packages/search?repo=keiailab-qdrant-operator
- **GHCR** (Container): https://github.com/keiailab/qdrant-operator/pkgs/container/qdrant-operator

## 11. License & Attribution(许可证与署名)

- License: [MIT](../LICENSE)
- Copyright: © 2026 keiailab contributors
- Third-party attributions: 如适用请参见 `NOTICE`

---

<p align="center">
  © 2026 keiailab · <a href="../LICENSE">MIT</a> · <a href="https://keiailab.com">keiailab.com</a>
</p>
