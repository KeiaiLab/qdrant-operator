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

本 Operator 将这一整套拼装与重新平衡过程,以 Kubernetes controller 的形式自动化。由于范围较大,开发被拆分为 5 个顺序推进的 Phase(见下方路线图)。

## 自定义资源

| Kind | 状态 | 作用 |
|---|---|---|
| `QdrantCluster` | Phase A(已实现) | 单实例或分布式(Raft)Qdrant 集群 |
| `QdrantCollection` | Phase B(计划中) | 支持 shard 重新平衡的声明式集合(collection) |

所有资源均使用 API 组 `qdrant.keiailab.com/v1alpha1`。

## Phase A 范围(当前)

本仓库当前仅实现 **Phase A:Operator 基础 + 预置(Provisioning)**。

**范围内**

- 通过 `QdrantCluster` CRD 对分布式 Qdrant 集群进行声明式的创建/更新/删除
- 与现有 helm chart 产出物保持一致性(parity)—— ServiceAccount、ConfigMap、headless Service、client Service、StatefulSet
- status 上报(`phase` / `readyReplicas` / `peers` / `conditions`)
- **scale-up**(简单的 replica 增加 —— 新 peer 加入 Raft)

**范围外(推迟到后续 Phase)**

- 集合(collection)/ shard 重新平衡 → Phase B
- 备份 / 恢复 → Phase C
- 支持 Raft 感知的滚动升级编排 → Phase D
- 自动扩缩容(Autoscaling) → Phase E

## 诚实的局限性(请务必阅读)

Phase A 是一个最小化的预置(provisioning)层,相较于"新增功能",它更看重"从结构上防止破坏性失误"。以下三种情况**属于有意不支持**的范围;发生时,operator 不会直接修改 StatefulSet,而是通过 `Degraded` condition + Event 安全地呈现出来。

1. **新扩容(scale-up)出来的 peer 会保持为空。** 增加 `spec.replicas` 会新增一个加入 Raft 的 StatefulSet pod,但已有的 shard 数据不会自动迁移到该 pod 上(开源版 Qdrant 没有 auto-resharding 能力)。在集合被显式重新创建或复制(replicate)到新 peer 之前,新 peer 实际上是一个空节点。该局限将在 **Phase B**(集合 / shard 编排)中得到解决。
2. **拒绝 scale-down。** 在分布式数据库中,简单地减少 replica 数会丢失序号(ordinal)最高的 peer 及其 shard。将 `spec.replicas` 调低到低于当前值的操作会被以 `Degraded` condition 拒绝,StatefulSet 保持不变。安全的、基于 drain 的 scale-down 要到 Phase B 才会支持。
3. **不支持修改 immutable 字段。** 涉及 StatefulSet immutable 字段(`serviceName` / `volumeClaimTemplates` / `selector`,例如 `persistence.size`)的 spec 变更,不会尝试 crash-loop 式的 patch,而是通过 `Degraded` condition + Event 呈现,StatefulSet 会被原样保留。可控的重建(recreate)是 Phase D 及之后的任务。

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
| **A** | Operator 基础 + 预置 | `QdrantCluster` | scaffold、controller、RBAC + 声明式分布式集群启动 | — | 进行中(本仓库) |
| **B** | 集合(Collection)/ shard 编排 | `QdrantCollection` | 声明式集合 + auto-rebalance(观测 → 规划 → `move_shard`)+ alias re-shard + 安全的 scale-in drain | A | 计划中 |
| **C** | 数据保护 | `QdrantBackup` / `QdrantRestore` | 基于 snapshot API 的定时备份、对象存储、恢复 | A | 计划中 |
| **D** | Day-2 / 升级 | (status / webhook) | 支持 Raft 感知的零停机滚动升级、health gate、可观测性(observability)、TLS | A | 计划中 |
| **E** | 自动扩缩容集成 | `QdrantAutoscaler` | 扩缩容触发器 → 接入 Phase B 的 rebalance 机制 | B | 计划中 |

依赖关系图:`A → {B, C, D}` 可以并行推进,`E` 则需要 `B` 先完成。Phase B(shard 重新平衡自动化)是本项目的核心价值所在,但必须先完成 Phase A —— 即 operator 掌握集群所有权的阶段。

## API

- Group / Version: `qdrant.keiailab.com/v1alpha1`
- Kind: `QdrantCluster`(Phase A)、`QdrantCollection`(Phase B,已完成 scaffold)
- Domain: `keiailab.com`(`kubebuilder init --domain keiailab.com --group qdrant`)

该 API 目前是 `v1alpha1`,在正式(stable)发布之前可能还会发生变更。

## 文档

- 开发指南(scaffold 结构、重新生成命令、controller 约定): [`AGENTS.md`](AGENTS.md)
- 设计文档(Phase B 及之后): [`docs/design/`](docs/design/)

## 发布

维护者通过单个命令同时发布到五个渠道 —— GitHub 标签(tag)、内部容器镜像、ghcr 容器镜像、ghcr OCI chart 和中央目录(catalog)—— 以避免遗漏任何一个渠道。

```bash
make release VERSION=0.7.0     # 关卡 → 打标签 → 镜像 → chart → 目录 → 验证
DRY_RUN=1 hack/release.sh 0.7.0  # 不发布,仅打印每一步
make verify-publish            # 检查当前状态下五个渠道的一致性
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
