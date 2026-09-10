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

本オペレーターがその制御ループです。プロビジョニングと shard オーケストレーションは実装済みで、バックアップ/リストアと Raft-aware なローリングアップグレードは未実装です(ロードマップは後述)。

## カスタムリソース

| Kind | ステータス | 概要 |
|---|---|---|
| `QdrantCluster` | 実装済み | スタンドアロンインスタンス、または分散(Raft)Qdrant クラスター — shard 再配置と安全な scale-in を含む |
| `QdrantCollection` | 実装済み | 宣言的コレクション — 作成・採用・alias ベースの re-shard |

すべてのリソースは API グループ `qdrant.keiailab.com/v1alpha1` を使用します。

## 何をするか

Phase A と B は実装済みで、本番稼働しています。

**プロビジョニング**

- `QdrantCluster` CRD による、分散 Qdrant クラスターの宣言的な作成・更新・削除
- 手書きマニフェストとのパリティ(parity)— ServiceAccount、ConfigMap、headless Service、client Service、StatefulSet、さらに `replicas >= 2` では PodDisruptionBudget
- Secret の `apiKey` / `readOnlyApiKey` を Qdrant の認証 env に配線し、TLS なしで鍵のみ設定された場合は `AuthWithoutTLS` で警告
- status レポーティング(`phase` / `readyReplicas` / `peers` / `shardDistribution` / `plannedMoves` / `conditions`)

**コレクション**

- `QdrantCollection` CRD による宣言的コレクション — 存在しなければ作成し、既にあれば **採用**します
- spec がライブのコレクションと食い違う場合は `Degraded(ParamsMismatch)` として可視化します。整合させるためにコレクションを再作成することはありません
- 既定は `onDelete: Retain` — CR を削除してもデータは削除されません
- `shardNumber` の変更は alias ベースの re-shard で処理します: shadow コレクション → コピー → 原子的な alias スワップ。スワップが唯一のコミット点です

**Shard 再配置**

- 観測 → 計画 → 実行の継続的な収束ループ。shard 操作は常に同時 1 件
- まず peer 間の shard **数** を揃え、数が均衡したら peer ごとの合計 **points** を二次基準として見ます
- 複製が不足した shard をコレクションの `replication_factor` まで回復し、健全な複製が戻ったら死んだ複製を回収します
- **scale-up**: 新しい peer が Raft に参加し、shard を自動的に受け取ります
- **scale-in**: 離脱する peer をドレイン(shard 移動 → 合意から除去 → StatefulSet 縮小)してから縮小します。切り捨てはしません
- 恒久的に故障したノードに取り残された pod は強制削除し、StatefulSet が代替 pod を作成できるようにします
- `spec.rebalance.enabled: false` は dry-run です — 計画は `status.plannedMoves` に公開され、発行だけ行いません

**オートスケーリング**

- `QdrantCluster` が `/scale` subresource を公開するため、KEDA `ScaledObject`(または HPA)が `spec.replicas` を直接操作し、shard の移動はオペレーターが担います

**未実装**

- バックアップ / リストア → Phase C
- Raft-aware なローリングアップグレードのオーケストレーション → Phase D

## 再配置の仕組み

すべての自動的な行為は、実行される **前に** 公開されます。事後に再構成すべき隠れた作業はありません。

```
観測                計画                            実行
─────────────────  ──────────────────────────────  ─────────────────────────
GET /cluster       1. 複製係数(RF)の回復           同時 1 件、
GET .../cluster    2. 死んだ複製の回収               着地を確認してから
peer ごとのサイズ    3. 数 → サイズ の順で均衡        最初から再計画
                   → status.plannedMoves
```

順序そのものが安全不変条件です。何かを削除する前に耐久性を回復し、使えない複製を配置として数えて均衡を誤判定することもありません。計画は毎回、現在の観測から一から算出され、完全に決定論的です。したがって中断されたオペレーターはキューを永続化せずとも同じ結論から再開します。

shard 操作は常に 1 件だけ進行します — 移動はネットワーク・ディスク・再インデックスのコストが大きく、並列に走らせれば助けようとしているクラスターを痛めます。均衡状態では書き込みを一切発行せず、読み取りのみを行います。

失敗は暗黙のリトライループではなく `Degraded` condition + Event として可視化され、condition は それを立てた経路自身が下ろします。

## 正直な制限事項(必ずお読みください)

本オペレーターは「新機能」よりも「破壊的なミスを構造的に防ぐこと」を重視します。

1. **immutable フィールドの変更は未サポートです。** StatefulSet の immutable フィールド(`serviceName` / `volumeClaimTemplates` / `selector`。例: `persistence.size`)に触れる spec 変更は、クラッシュループする patch を試みる代わりに `Degraded` condition + Event として可視化され、StatefulSet はそのまま保持されます。制御された recreate は Phase D の課題です。
2. **コレクションを再作成することはありません。** `QdrantCollection` の spec がライブのコレクションと vector size・distance・replication factor で食い違う場合、`ParamsMismatch` を報告して停止します。非破壊的な移行経路があるのは `shardNumber` だけです(alias re-shard)。
3. **健全な余剰複製は削除しません。** `replication_factor` を超える replica は観測・報告のみで、自動的に drop しません。死んだ複製だけが例外です — サービスに使われず、健全な複製が複製係数を満たした後にのみ回収されます。
4. **サイズを考慮した配置には全 peer への到達が必要です。** 二次基準となる peer ごとの points は peer を 1 つずつ辿って収集します。1 つでも応答しない場合、部分的な地図で判断する代わりにサイズ段階全体をスキップします。

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
| **A** | オペレーター基盤 + プロビジョニング | `QdrantCluster` | scaffold・controller・RBAC + 宣言的な分散クラスター起動 | — | **完了** |
| **B** | コレクション / shard オーケストレーション | `QdrantCollection` | 宣言的コレクション + auto-rebalance(観測 → 計画 → `move_shard`)+ 複製係数の修復 + alias re-shard + 安全な scale-in drain | A | **完了** |
| **C** | データ保護 | `QdrantBackup` / `QdrantRestore` | snapshot API によるスケジュールバックアップ・オブジェクトストレージ・リストア | A | 計画中 |
| **D** | Day-2 / アップグレード | (status / webhook) | Raft-aware な無停止ローリングアップグレード・health gate・observability・TLS | A | 計画中 |
| **E** | オートスケーリング統合 | (`/scale` subresource) | スケールトリガー → Phase B の rebalance 機構に接続 | B | **完了** — `QdrantCluster` を KEDA や HPA が直接スケールします。専用 CRD は不要でした |

依存グラフ: `A → {B, C, D}` は並行して進めることができ、`E` は `B` の完了を必要とします。Phase B(shard 再配置の自動化)が本プロジェクトの中核的価値です。

Phase E は `QdrantAutoscaler` CRD ではなく `/scale` subresource として着地しました。KEDA は `/scale` を公開する custom resource であれば何でもスケールできるため、独自のオートスケーラーは再発明になります。KEDA が判断し、オペレーターが実行します。

## API

- Group / Version: `qdrant.keiailab.com/v1alpha1`
- Kind: `QdrantCluster`、`QdrantCollection`
- Domain: `keiailab.com`(`kubebuilder init --domain keiailab.com --group qdrant`)

API は `v1alpha1` であり、stable リリースまでに変更される可能性があります。

## ドキュメント

- 開発ガイド(scaffold 構造・再生成コマンド・controller の規約): [`AGENTS.md`](AGENTS.md)
- 設計ドキュメント(Phase B 以降): [`docs/design/`](docs/design/)

## リリース

メンテナーは、GitHub タグ・ghcr コンテナイメージ・ghcr OCI chart・中央カタログの 4 つのチャネルへ単一のコマンドで同時に公開します — チャネルの publish 漏れを防ぐためです。

```bash
make release VERSION=0.9.0     # ゲート → タグ → イメージ → chart → カタログ → 検証
DRY_RUN=1 hack/release.sh 0.9.0  # 公開せずに全ステップを出力
make verify-publish            # 現在の状態が 4 チャネルで一致しているかを検査
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
