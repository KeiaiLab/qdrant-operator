<p align="center">
  <a href="https://keiailab.com">
    <img src="docs/branding/symbol.png" alt="qdrant-operator" width="96"/>
  </a>
</p>

# qdrant-operator

> **通过单个 `QdrantCluster` 资源来预置(provision)并运维分布式 [Qdrant](https://qdrant.tech) 向量数据库集群的 Kubernetes Operator。MIT 许可证。**

<p align="center">
  <a href="LICENSE"><img src="https://img.shields.io/badge/License-MIT-blue.svg" alt="License: MIT"/></a>
  <a href="go.mod"><img src="https://img.shields.io/badge/Go-1.26-00ADD8?logo=go&logoColor=white" alt="Go"/></a>
  <a href="https://qdrant.tech/"><img src="https://img.shields.io/badge/Qdrant-1.18%2B-DC244C?logo=qdrant&logoColor=white" alt="Qdrant"/></a>
  <a href="https://kubernetes.io/"><img src="https://img.shields.io/badge/Kubernetes-1.29%2B-326CE5?logo=kubernetes&logoColor=white" alt="Kubernetes"/></a>
</p>

<p align="center">
  <a href="README.md">English</a> ·
  <a href="README.ko.md">한국어</a> ·
  <a href="README.ja.md">日本語</a> ·
  <b>中文</b>
</p>

## 设计资产

| 资产 | 路径 | 用途 |
|---|---|---|
| 居中服务符号 | [`docs/branding/symbol.png`](docs/branding/symbol.png) | GitHub README、Artifact Hub 图标 |
| Keiailab 基础符号 | [`docs/branding/base-symbol.png`](docs/branding/base-symbol.png) | 外圈旋转箭头标记的源参考 |
| 品牌指南 | [`docs/BRANDING.md`](docs/BRANDING.md) | 公开视觉资产使用规则 |

## 为什么需要

在 Kubernetes 上自行运维 self-hosted Qdrant,意味着要手工拼装 StatefulSet、Service、ConfigMap 和 PVC,并且每次增删节点时都要手动迁移 shard。Qdrant 已经以公开 API 的形式提供了你所需要的全部原语(Raft peer join、`move_shard`、`replicate_shard`、collection alias),但把这些原语串联起来、并收敛到期望状态的控制循环,仍然要靠运维人员自己完成。

本 Operator 就是这个控制循环。预置与 shard 编排已经实现,备份/恢复以及 Raft 感知的滚动升级尚未实现(见下方路线图)。

## 自定义资源

| Kind | 状态 | 作用 |
|---|---|---|
| `QdrantCluster` | 已实现 | 单实例或分布式(Raft)Qdrant 集群 —— 含 shard 重新平衡与安全的 scale-in |
| `QdrantCollection` | 已实现 | 声明式集合(collection)—— 创建、接管、基于 alias 的 re-shard |

所有资源均使用 API 组 `qdrant.keiailab.com/v1alpha1`。

## 它做什么

Phase A 与 B 均已实现并在生产环境运行。

**预置(Provisioning)**

- 通过 `QdrantCluster` CRD 对分布式 Qdrant 集群进行声明式的创建/更新/删除
- 与手写清单保持一致性(parity)—— ServiceAccount、ConfigMap、headless Service、client Service、StatefulSet,以及 `replicas >= 2` 时的 PodDisruptionBudget
- 将 Secret 中的 `apiKey` / `readOnlyApiKey` 接入 Qdrant 认证 env;若设置了密钥却未启用 TLS,则以 `AuthWithoutTLS` 告警
- status 上报(`phase` / `readyReplicas` / `peers` / `shardDistribution` / `plannedMoves` / `conditions`)

**集合(Collection)**

- 通过 `QdrantCollection` CRD 声明式管理集合 —— 不存在则创建,已存在则**接管**
- 当 spec 与线上集合不一致时,以 `Degraded(ParamsMismatch)` 呈现;operator 绝不会为了对齐而重建集合
- 默认 `onDelete: Retain` —— 删除 CR 不会删除你的数据
- 修改 `shardNumber` 走基于 alias 的 re-shard:影子集合 → 复制 → 原子 alias 切换,切换是唯一的提交点

**Shard 重新平衡**

- 观测 → 计划 → 执行的持续收敛循环,shard 操作始终同时仅 1 件
- 先在 peer 之间对齐 shard **数量**,数量均衡后再以每个 peer 的总 **points** 作为二级标准
- 将副本不足的 shard 恢复到集合的 `replication_factor`,健全副本回归后回收失效副本
- **scale-up**:新 peer 加入 Raft 并自动接收 shard
- **scale-in**:先排空(drain)待下线的 peer(迁移 shard → 从共识中移除 → 缩容 StatefulSet),而不是直接截断
- 被困在永久故障节点上的 pod 会被强制删除,让 StatefulSet 能够重建替代 pod
- `spec.rebalance.enabled: false` 即 dry-run —— 计划照常发布到 `status.plannedMoves`,只是不下发

**自动扩缩容**

- `QdrantCluster` 暴露 `/scale` subresource,因此 KEDA `ScaledObject`(或 HPA)可直接驱动 `spec.replicas`,shard 迁移交给 operator 处理

**未实现**

- 备份 / 恢复 → Phase C
- 支持 Raft 感知的滚动升级编排 → Phase D

## 重新平衡如何运作

所有自动行为都在执行**之前**先行公布。不存在需要事后追溯还原的隐藏动作。

```
观测                计划                            执行
─────────────────  ──────────────────────────────  ─────────────────────────
GET /cluster       1. 恢复复制因子(RF)             同时 1 件,
GET .../cluster    2. 回收失效副本                   确认落地后
每个 peer 的规模     3. 先数量、后规模的均衡           从头重新计划
                   → status.plannedMoves
```

顺序本身就是安全不变式:在删除任何东西之前先恢复持久性,并且绝不把不可用的副本计入布局而误判均衡。计划在每一轮都从当前观测重新算出,且完全确定性 —— 因此被中断的 operator 无需持久化队列也能回到同一结论。

任一时刻只有一个 shard 操作在进行 —— 迁移在网络、磁盘与重建索引上代价高昂,并行执行只会伤害你本想帮助的集群。均衡状态下 operator 完全不发起写入,只做读取。

失败不会陷入静默重试循环,而是以 `Degraded` condition + Event 呈现;condition 由点亮它的那条路径自己熄灭。

## 诚实的局限性(请务必阅读)

相较于"新增功能",本 Operator 更看重"从结构上防止破坏性失误"。

1. **不支持修改 immutable 字段。** 涉及 StatefulSet immutable 字段(`serviceName` / `volumeClaimTemplates` / `selector`,例如 `persistence.size`)的 spec 变更,不会尝试 crash-loop 式的 patch,而是通过 `Degraded` condition + Event 呈现,StatefulSet 会被原样保留。可控的重建(recreate)是 Phase D 的任务。
2. **绝不重建集合。** 若 `QdrantCollection` 的 spec 与线上集合在 vector size、distance 或 replication factor 上不一致,operator 会报告 `ParamsMismatch` 并停下。只有 `shardNumber` 存在非破坏性的迁移路径(alias re-shard)。
3. **不会删除健全的多余副本。** 超出 `replication_factor` 的 replica 只做观测与上报,绝不自动 drop。失效副本是唯一的例外 —— 它不承担任何服务,并且只有在健全副本满足复制因子之后才会被回收。
4. **按规模分布需要所有 peer 可达。** 驱动二级均衡标准的每 peer points 是逐个 peer 收集的;只要有一个 peer 没有响应,就整体跳过规模阶段,而不是依据不完整的映射去行动。

出于数据安全考虑,operator **有意不持有(own)** PVC(`volumeClaimTemplates`)—— 删除 `QdrantCluster` 后 PVC 会被保留下来(仅当设置 `persistence.retentionPolicy: Delete` 时才会被回收)。

## 安装

该 Operator 通过其自带的 Helm chart 部署。

```sh
helm install qdrant-operator ./deploy/chart \
  --namespace qdrant-operator-system --create-namespace
```

此过程会安装 CRD、RBAC,以及启用了 leader-election 的 controller-manager Deployment。容器镜像发布于 `ghcr.io/keiailab/qdrant-operator`。

### 从源代码安装

```sh
make install                                   # 安装 CRD
make deploy IMG=ghcr.io/keiailab/qdrant-operator:latest
```

## 使用方法

```yaml
apiVersion: qdrant.keiailab.com/v1alpha1
kind: QdrantCluster
metadata:
  name: my-qdrant
  namespace: data
spec:
  image:
    repository: qdrant/qdrant   # 默认值
    tag: v1.18.2                # 默认值
  replicas: 3
  resources:
    requests: { cpu: 250m, memory: 512Mi }
    limits:   { cpu: "2",  memory: 4Gi }
  persistence:
    size: 10Gi                   # 默认值
    storageClassName: ceph-rbd   # 默认值
    accessModes: [ReadWriteOnce] # 默认值
    retentionPolicy: Retain      # 默认值 — Retain | Delete
  config:
    clusterEnabled: true   # 默认值 — 分布式(Raft)模式
    tlsEnabled: false
    # rawOverride: {}      # production.yaml 的逃生舱口(escape hatch),用于罕见的 upstream 选项
  serviceType: ClusterIP   # 默认值
  apiKey:
    name: my-qdrant-api-key  # Secret 名称(必需)
    key: api-key              # 默认值
  runAsUser: 1000  # 默认值
  fsGroup: 3000    # 默认值
```

应用后检查状态:

```sh
kubectl apply -f qdrantcluster.yaml
kubectl get qdrantcluster my-qdrant -n data -o jsonpath='{.status.phase}'
```

## 路线图

| Phase | 子系统 | 关键 CRD | 内容 | 依赖 | 状态 |
|---|---|---|---|---|---|
| **A** | Operator 基础 + 预置 | `QdrantCluster` | scaffold、controller、RBAC + 声明式分布式集群启动 | — | **已完成** |
| **B** | 集合(Collection)/ shard 编排 | `QdrantCollection` | 声明式集合 + auto-rebalance(观测 → 规划 → `move_shard`)+ 复制因子修复 + alias re-shard + 安全的 scale-in drain | A | **已完成** |
| **C** | 数据保护 | `QdrantBackup` | 全 peer 的 snapshot API 定时备份、S3 对象存储、保留策略 | A | **备份已完成**,恢复进行中 |
| **D** | Day-2 / 升级 | (status / webhook) | 支持 Raft 感知的零停机滚动升级、health gate、可观测性(observability)、TLS | A | 计划中 |
| **E** | 自动扩缩容集成 | (`/scale` subresource) | 扩缩容触发器 → 接入 Phase B 的 rebalance 机制 | B | **已完成** —— KEDA 或 HPA 可直接扩缩 `QdrantCluster`,无需专用 CRD |

依赖关系图:`A → {B, C, D}` 可以并行推进,`E` 则需要 `B` 先完成。Phase B(shard 重新平衡自动化)是本项目的核心价值所在。

Phase E 最终落地为 `/scale` subresource,而非 `QdrantAutoscaler` CRD:KEDA 本就能扩缩任何暴露 `/scale` 的自定义资源,再造一个自动扩缩容器只是重复发明。KEDA 负责决策,operator 负责执行。

## API

- Group / Version: `qdrant.keiailab.com/v1alpha1`
- Kind: `QdrantCluster`、`QdrantCollection`
- Domain: `keiailab.com`(`kubebuilder init --domain keiailab.com --group qdrant`)

该 API 目前是 `v1alpha1`,在正式(stable)发布之前可能还会发生变更。

## 文档

- 开发指南(scaffold 结构、重新生成命令、controller 约定): [`AGENTS.md`](AGENTS.md)
- 设计文档(Phase B 及之后): [`docs/design/`](docs/design/)

## 发布

维护者通过单个命令同时发布到四个渠道 —— GitHub 标签(tag)、ghcr 容器镜像、ghcr OCI chart 和中央目录(catalog)—— 以避免遗漏任何一个渠道。

```bash
make release VERSION=0.9.0     # 关卡 → 打标签 → 镜像 → chart → 目录 → 验证
DRY_RUN=1 hack/release.sh 0.9.0  # 不发布,仅打印每一步
make verify-publish            # 检查当前状态下四个渠道的一致性
```

release gate 会先运行 `test`、`lint` 和 `publish-scan`,再用 `verify-publish`
复核结果 —— 只要其中任意一项失败,发布就会中止。

## 贡献

欢迎贡献。如果改动并非细枝末节,请先开一个 issue,以便就 API 界面(surface)达成一致意见。详见 [CONTRIBUTING.md](.github/CONTRIBUTING.md),完整的构建目标列表可通过 `make help` 查看。

如需报告安全问题,请遵循 [SECURITY.md](.github/SECURITY.md) 中的流程,而不要直接创建公开 issue。

## 许可证

[MIT](LICENSE) © keiailab

---

<p align="center">© 2026 keiailab · MIT · <a href="https://keiailab.com">keiailab.com</a></p>
