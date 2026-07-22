<p align="center">
  <a href="https://keiailab.com">
    <img src="docs/branding/symbol.png" alt="qdrant-operator" width="96"/>
  </a>
</p>

# qdrant-operator

> **単一の `QdrantCluster` リソースから分散 [Qdrant](https://qdrant.tech) ベクトルデータベースクラスターをプロビジョニング・運用する Kubernetes オペレーター。MIT ライセンス。**

<p align="center">
  <a href="LICENSE"><img src="https://img.shields.io/badge/License-MIT-blue.svg" alt="License: MIT"/></a>
  <a href="go.mod"><img src="https://img.shields.io/badge/Go-1.26-00ADD8?logo=go&logoColor=white" alt="Go"/></a>
  <a href="https://qdrant.tech/"><img src="https://img.shields.io/badge/Qdrant-1.18%2B-DC244C?logo=qdrant&logoColor=white" alt="Qdrant"/></a>
  <a href="https://kubernetes.io/"><img src="https://img.shields.io/badge/Kubernetes-1.29%2B-326CE5?logo=kubernetes&logoColor=white" alt="Kubernetes"/></a>
</p>

<p align="center">
  <a href="README.md">English</a> ·
  <a href="README.ko.md">한국어</a> ·
  <b>日本語</b> ·
  <a href="README.zh.md">中文</a>
</p>

## デザインアセット

| アセット | パス | 用途 |
|---|---|---|
| 中央配置のサービスシンボル | [`docs/branding/symbol.png`](docs/branding/symbol.png) | GitHub README、Artifact Hub アイコン |
| Keiailab ベースシンボル | [`docs/branding/base-symbol.png`](docs/branding/base-symbol.png) | 外周の回転矢印マークのソースリファレンス |
| ブランディングガイド | [`docs/BRANDING.md`](docs/BRANDING.md) | 公開時の visual asset 使用ルール |

## なぜ必要か

self-hosted Qdrant を Kubernetes 上で運用するには、StatefulSet、Service、ConfigMap、PVC を手作業で組み立てる必要があり、ノードを追加・削除するたびに shard の再配置を手動で行う必要があります。Qdrant はそのために必要なプリミティブ(Raft peer join・`move_shard`・`replicate_shard`・collection alias)をすべて公開 API として提供していますが、それらを結び付けて目的の状態へ収束させる制御ループはオペレーター側に委ねられています。

本オペレーターは、その組み立てと再配置を Kubernetes コントローラーとして自動化します。スコープが大きいため、5 つの逐次 Phase に分けて開発します(ロードマップは後述)。

## カスタムリソース

| Kind | ステータス | 概要 |
|---|---|---|
| `QdrantCluster` | Phase A(実装済み) | スタンドアロンインスタンス、または分散(Raft)Qdrant クラスター |
| `QdrantCollection` | Phase B(計画中) | shard 再配置を伴う宣言的コレクション |

すべてのリソースは API グループ `qdrant.keiailab.com/v1alpha1` を使用します。

## Phase A のスコープ(現在)

本リポジトリは現在、**Phase A: オペレーター基盤 + プロビジョニング** のみを実装しています。

**対象範囲**

- `QdrantCluster` CRD による、分散 Qdrant クラスターの宣言的な作成・更新・削除
- 既存の helm チャート出力とのパリティ(parity)— ServiceAccount、ConfigMap、headless Service、client Service、StatefulSet
- status レポーティング(`phase` / `readyReplicas` / `peers` / `conditions`)
- **scale-up**(naive な replica 増加 — 新しい peer が Raft に join)

**対象外(後続 Phase へ移管)**

- コレクション / shard rebalance → Phase B
- バックアップ / リストア → Phase C
- Raft-aware なローリングアップグレードのオーケストレーション → Phase D
- オートスケーリング → Phase E

## 正直な制限事項(必ずお読みください)

Phase A は「新機能」よりも「破壊的なミスを構造的に防ぐこと」を重視した最小限のプロビジョニング層です。次の 3 つのケースは **意図的に未サポート** としており、StatefulSet を直接書き換える代わりに `Degraded` condition + Event として安全に可視化します。

1. **新規に scale-up された peer は空のまま残ります。** `spec.replicas` を増やすと、新しい StatefulSet pod が追加されて Raft に join しますが、既存の shard データは自動的には移動しません(OSS 版 Qdrant には auto-resharding 機能がありません)。新しい peer は、コレクションが明示的に再作成されるか replicate されるまで、事実上空のノードのままです。この制限は **Phase B**(コレクション / shard オーケストレーション)で解消されます。
2. **scale-down は拒否されます。** 分散データベースにおいて、単純な replica 数の削減は最も序数(ordinal)の大きい peer とその shard を失うことにつながります。`spec.replicas` を現在値より下げようとすると `Degraded` condition で拒否され、StatefulSet は変更されません。安全な drain ベースの scale-down は Phase B までサポートされません。
3. **immutable フィールドの変更は未サポートです。** StatefulSet の immutable フィールド(`serviceName` / `volumeClaimTemplates` / `selector`。例: `persistence.size`)に触れる spec 変更は、クラッシュループする patch を試みる代わりに `Degraded` condition + Event として可視化され、StatefulSet はそのまま保持されます。制御された recreate は Phase D 以降の課題です。

データ安全性のため、PVC(`volumeClaimTemplates`)はオペレーターが **意図的に所有しません** — `QdrantCluster` を削除しても PVC は残ります(`persistence.retentionPolicy: Delete` を設定した場合にのみ回収されます)。

## インストール

オペレーターは専用の Helm チャートでデプロイします。

```sh
helm install qdrant-operator ./deploy/chart \
  --namespace qdrant-operator-system --create-namespace
```

これにより CRD、RBAC、そして leader-election が有効な controller-manager Deployment がインストールされます。コンテナイメージは `ghcr.io/keiailab/qdrant-operator` で公開されています。

### ソースからインストール

```sh
make install                                   # CRD をインストール
make deploy IMG=ghcr.io/keiailab/qdrant-operator:latest
```

## 使い方

```yaml
apiVersion: qdrant.keiailab.com/v1alpha1
kind: QdrantCluster
metadata:
  name: my-qdrant
  namespace: data
spec:
  image:
    repository: qdrant/qdrant   # デフォルト
    tag: v1.18.2                # デフォルト
  replicas: 3
  resources:
    requests: { cpu: 250m, memory: 512Mi }
    limits:   { cpu: "2",  memory: 4Gi }
  persistence:
    size: 10Gi                   # デフォルト
    storageClassName: ceph-rbd   # デフォルト
    accessModes: [ReadWriteOnce] # デフォルト
    retentionPolicy: Retain      # デフォルト — Retain | Delete
  config:
    clusterEnabled: true   # デフォルト — 分散(Raft)モード
    tlsEnabled: false
    # rawOverride: {}      # production.yaml のエスケープハッチ(稀な upstream オプション向け)
  serviceType: ClusterIP   # デフォルト
  apiKey:
    name: my-qdrant-api-key  # Secret 名(必須)
    key: api-key             # デフォルト
  runAsUser: 1000  # デフォルト
  fsGroup: 3000    # デフォルト
```

適用してステータスを確認します:

```sh
kubectl apply -f qdrantcluster.yaml
kubectl get qdrantcluster my-qdrant -n data -o jsonpath='{.status.phase}'
```

## ロードマップ

| Phase | サブシステム | 主要 CRD | 内容 | 依存 | ステータス |
|---|---|---|---|---|---|
| **A** | オペレーター基盤 + プロビジョニング | `QdrantCluster` | scaffold・controller・RBAC + 宣言的な分散クラスター起動 | — | 進行中(本リポジトリ) |
| **B** | コレクション / shard オーケストレーション | `QdrantCollection` | 宣言的コレクション + auto-rebalance(観測 → 計画 → `move_shard`)+ alias re-shard + 安全な scale-in drain | A | 計画中 |
| **C** | データ保護 | `QdrantBackup` / `QdrantRestore` | snapshot API によるスケジュールバックアップ・オブジェクトストレージ・リストア | A | 計画中 |
| **D** | Day-2 / アップグレード | (status / webhook) | Raft-aware な無停止ローリングアップグレード・health gate・observability・TLS | A | 計画中 |
| **E** | オートスケーリング統合 | `QdrantAutoscaler` | スケールトリガー → Phase B の rebalance 機構に接続 | B | 計画中 |

依存グラフ: `A → {B, C, D}` は並行して進めることができ、`E` は `B` の完了を必要とします。Phase B(shard 再配置の自動化)が本プロジェクトの中核的価値ですが、オペレーターがクラスターの所有権を持つ Phase A が必ず先行しなければなりません。

## API

- Group / Version: `qdrant.keiailab.com/v1alpha1`
- Kind: `QdrantCluster`(Phase A)、`QdrantCollection`(Phase B、スキャフォールド済み)
- Domain: `keiailab.com`(`kubebuilder init --domain keiailab.com --group qdrant`)

API は `v1alpha1` であり、stable リリースまでに変更される可能性があります。

## ドキュメント

- 開発ガイド(scaffold 構造・再生成コマンド・controller の規約): [`AGENTS.md`](AGENTS.md)
- 設計ドキュメント(Phase B 以降): [`docs/design/`](docs/design/)

## リリース

メンテナーは、GitHub タグ・内部コンテナイメージ・ghcr コンテナイメージ・ghcr OCI chart・中央カタログの 5 つのチャネルへ単一のコマンドで同時に公開します — チャネルの publish 漏れを防ぐためです。

```bash
make release VERSION=0.7.0     # ゲート → タグ → イメージ → chart → カタログ → 検証
DRY_RUN=1 hack/release.sh 0.7.0  # 公開せずに全ステップを出力
make verify-publish            # 現在の状態が 5 チャネルで一致しているかを検査
```

リリースゲートはまず `test`・`lint`・`publish-scan` を実行し、その結果を `verify-publish` で
再確認します — いずれか 1 つでも失敗すると、リリースは中断されます。

## コントリビューション

コントリビューションを歓迎します。些細ではない変更については、API サーフェスについて事前に合意できるよう、まず issue を作成してください。詳細は [CONTRIBUTING.md](.github/CONTRIBUTING.md) を参照し、ビルドターゲットの全一覧は `make help` で確認してください。

セキュリティ上の問題を報告する場合は、公開 issue を作成するのではなく [SECURITY.md](.github/SECURITY.md) の手順に従ってください。

## ライセンス

[MIT](LICENSE) © keiailab

---

<p align="center">© 2026 keiailab · MIT · <a href="https://keiailab.com">keiailab.com</a></p>
