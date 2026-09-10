<p align="center">
  <a href="https://keiailab.com">
    <img src="docs/branding/symbol.png" alt="qdrant-operator" width="96"/>
  </a>
</p>

# qdrant-operator

> **단일 `QdrantCluster` 리소스로 분산 [Qdrant](https://qdrant.tech) 벡터 데이터베이스 클러스터를 프로비저닝·운영하는 Kubernetes 오퍼레이터. MIT 라이선스.**

<p align="center">
  <a href="LICENSE"><img src="https://img.shields.io/badge/License-MIT-blue.svg" alt="License: MIT"/></a>
  <a href="go.mod"><img src="https://img.shields.io/badge/Go-1.26-00ADD8?logo=go&logoColor=white" alt="Go"/></a>
  <a href="https://qdrant.tech/"><img src="https://img.shields.io/badge/Qdrant-1.18%2B-DC244C?logo=qdrant&logoColor=white" alt="Qdrant"/></a>
  <a href="https://kubernetes.io/"><img src="https://img.shields.io/badge/Kubernetes-1.29%2B-326CE5?logo=kubernetes&logoColor=white" alt="Kubernetes"/></a>
</p>

<p align="center">
  <a href="README.md">English</a> ·
  <b>한국어</b> ·
  <a href="README.ja.md">日本語</a> ·
  <a href="README.zh.md">中文</a>
</p>

## 디자인 자산

| 자산 | 경로 | 용도 |
|---|---|---|
| 중앙 서비스 심볼 | [`docs/branding/symbol.png`](docs/branding/symbol.png) | GitHub README, Artifact Hub 아이콘 |
| Keiailab 베이스 심볼 | [`docs/branding/base-symbol.png`](docs/branding/base-symbol.png) | 바깥쪽 회전 화살표 마크의 소스 레퍼런스 |
| 브랜딩 가이드 | [`docs/BRANDING.md`](docs/BRANDING.md) | 공개 시각 자산 사용 규칙 |

## 왜 필요한가

self-hosted Qdrant를 Kubernetes에서 운영하려면 StatefulSet · Service · ConfigMap · PVC를 직접 조립해야 하고, 노드를 늘리거나 줄일 때마다 shard 재배치를 수동으로 수행해야 한다. Qdrant는 이에 필요한 프리미티브(Raft peer join · `move_shard` · `replicate_shard` · collection alias)를 모두 공개 API로 제공하지만, 이를 엮어 원하는 상태로 수렴시키는 컨트롤 루프는 운영자 몫으로 남는다.

이 오퍼레이터가 그 컨트롤 루프다. 프로비저닝과 shard 오케스트레이션은 구현됐고, 백업/복원과 Raft-aware 롤링 업그레이드는 아직이다(아래 로드맵 참고).

## 커스텀 리소스

| Kind | 상태 | 하는 일 |
|---|---|---|
| `QdrantCluster` | 구현됨 | 단일 인스턴스 또는 분산(Raft) Qdrant 클러스터 — shard 재배치와 안전한 scale-in 포함 |
| `QdrantCollection` | 구현됨 | 선언적 컬렉션 — 생성 · 채택 · alias 기반 re-shard |
| `QdrantBackup` | 구현됨 | 전 peer 스냅샷 스케줄 백업 · 보존기간 |
| `QdrantRestore` | 구현됨 | 백업 세대에서 컬렉션 복원 |

모든 리소스는 API 그룹 `qdrant.keiailab.com/v1alpha1`을 사용한다.

## 무엇을 하는가

Phase A와 B가 구현됐고 운영 중이다.

**프로비저닝**

- `QdrantCluster` CRD를 통한 분산 Qdrant 클러스터의 선언적 생성/수정/삭제
- 손으로 쓴 매니페스트와의 동등성(parity) — ServiceAccount, ConfigMap, headless Service, client Service, StatefulSet, 그리고 `replicas >= 2`에서 PodDisruptionBudget
- Secret의 `apiKey` / `readOnlyApiKey`를 Qdrant 인증 env로 배선하고, TLS 없이 키만 설정하면 `AuthWithoutTLS`로 경고
- status 보고 (`phase` / `readyReplicas` / `peers` / `shardDistribution` / `plannedMoves` / `conditions`)

**컬렉션**

- `QdrantCollection` CRD를 통한 선언적 컬렉션 — 없으면 생성, 이미 있으면 **채택**
- spec이 라이브 컬렉션과 어긋나면 `Degraded(ParamsMismatch)`로 표면화한다. 맞추려고 컬렉션을 재생성하는 일은 없다
- 기본값 `onDelete: Retain` — CR을 지워도 데이터는 지워지지 않는다
- `shardNumber` 변경은 alias 기반 re-shard로 처리한다: shadow 컬렉션 → 복사 → 원자 alias 스왑. 스왑이 유일한 커밋점이다

**Shard 재배치**

- 관측 → 계획 → 실행의 상시 수렴 루프. shard 연산은 항상 동시 1건
- peer 간 shard **개수**를 먼저 맞추고, 개수가 균형에 이르면 peer별 총 **points**를 2차 기준으로 본다
- 복제본이 모자란 shard를 컬렉션의 `replication_factor`까지 되메우고, 성한 사본이 돌아오면 죽은 사본을 회수한다
- **scale-up**: 새 peer가 Raft에 합류하고 shard를 자동으로 받는다
- **scale-in**: 떠날 peer를 드레인한 뒤(shard 이동 → 합의에서 제거 → StatefulSet 축소) 줄인다. 잘라내지 않는다
- 영구 장애 노드에 갇힌 파드는 강제 삭제해 StatefulSet이 대체 파드를 만들게 한다
- **Raft-aware 롤링 업그레이드**: StatefulSet 롤아웃을 오퍼레이터가 서수 하나씩 쥐고, 재기동한 peer가 합의에 복귀하고 전 shard가 다시 `Active`가 된 뒤에만 다음으로 넘어간다. 그동안 재배치는 멈춘다
- `spec.rebalance.enabled: false`는 dry-run이다 — 계획은 `status.plannedMoves`에 그대로 노출되고 발행만 하지 않는다

**오토스케일링**

- `QdrantCluster`가 `/scale` subresource를 노출하므로 KEDA `ScaledObject`(또는 HPA)가 `spec.replicas`를 직접 조정하고 shard 이동은 오퍼레이터가 맡는다

**미구현**

- 백업 / 복원 → Phase C
- Raft-aware 롤링 업그레이드 오케스트레이션 → Phase D

## 재배치는 이렇게 돈다

모든 자동 행위는 실행 **전에** 노출된다. 사후에 재구성해야 하는 숨은 작업은 없다.

```
관측                계획                            실행
─────────────────  ──────────────────────────────  ─────────────────────────
GET /cluster       1. 복제 계수(RF) 회복             동시 1건,
GET .../cluster    2. 죽은 사본 회수                 완료를 확인한 뒤
peer별 크기         3. 개수 → 크기 순 균형            처음부터 다시 계획
                   → status.plannedMoves
```

순서가 곧 안전 불변식이다. 무언가를 지우기 전에 내구성을 먼저 회복하고, 못 쓰는 사본을 배치로 세어 균형을 오판하지 않는다. 계획은 매 회차 현재 관측에서 처음부터 다시 산출되고 완전히 결정론적이라, 중단된 오퍼레이터는 큐를 저장하지 않고도 같은 결론에서 재개한다.

shard 연산은 항상 하나만 진행한다 — 이동은 네트워크·디스크·재색인 비용이 크고, 동시에 돌리면 도우려던 클러스터를 해친다. 균형 상태에서는 쓰기를 전혀 발행하지 않고 읽기만 한다.

실패는 조용한 재시도 루프가 아니라 `Degraded` condition + Event로 표면화되며, 조건은 그것을 켠 경로가 끈다.

## 백업과 복원

qdrant 의 스냅샷은 노드 단위다 — 그것을 만든 peer 가 가진 shard 만 담는다. 그래서 컬렉션 하나의 백업은 전 peer 스냅샷의 집합이고, `QdrantBackup` 이 그 팬아웃을 맡는다.

```yaml
apiVersion: qdrant.keiailab.com/v1alpha1
kind: QdrantBackup
metadata: { name: nightly, namespace: data }
spec:
  clusterRef: my-qdrant
  collections: []
  schedule: "0 3 * * *"
  retention:
    keepLast: 7
```

`collections` 를 비우면 클러스터의 전 컬렉션이 대상이고, `schedule` 을 비우면 1회성이며, `retention` 을 생략하면 **아무것도 지우지 않는다.**

생성은 비동기로 발행하고 완료는 스냅샷 목록 관측으로 판정한다 — 큰 컬렉션은 어떤 HTTP 타임아웃보다도 오래 걸린다. 스냅샷은 동시 1건만 진행한다. 해석할 수 없는 cron 은 조용히 안 도는 대신 `Degraded` 로 표면화된다.

스냅샷 보관을 S3 호환 버킷으로 돌리면 qdrant 가 직접 그곳에 쓴다. 오퍼레이터는 바이트를 만지지 않는다.

```yaml
spec:
  snapshots:
    storage: S3
    s3:
      bucket: qdrant-backups
      endpointURL: http://rook-ceph-rgw-my-store.rook-ceph.svc:80
      credentials:
        name: qdrant-backup-s3
```

클러스터와 그 엔드포인트 사이에 NetworkPolicy 가 있다면 서비스 포트가 아니라 **DNAT 된 파드 포트**를 열어야 한다 — 틀리면 403 이 아니라 무응답으로 나타난다.

복원은 별도의 1회성 리소스다.

```yaml
apiVersion: qdrant.keiailab.com/v1alpha1
kind: QdrantRestore
spec:
  clusterRef: my-qdrant
  collection: my-vectors
  fromBackup: nightly
  priority: snapshot
```

복원을 위해 무엇도 지우지 않는다. qdrant 문서는 컬렉션을 지웠다 다시 만들라고 하지만 그 삭제는 되돌릴 수 없다 — `priority: snapshot` 이 같은 결과를 파괴 없이 낸다. 완료된 `QdrantRestore` 는 다시 돌지 않는다. 재실행은 그 뒤에 쓰인 데이터를 조용히 되돌린다.

## 관측 지표

컨트롤러는 controller-runtime 표준 엔드포인트로 Prometheus 지표를 낸다. 목록은 의도적으로 짧다 — **값이 변하면 사람이 무언가 해야 하는 것**만 넣었다. shard 분포나 이동 계획은 `status` 에 남는다. 그것은 이미 볼 이유가 생긴 뒤에 보는 것이다.

| 지표 | 무엇을 말하는가 |
|---|---|
| `qdrant_operator_backup_last_success_timestamp_seconds` | 백업이 조용히 멈춘 것. 이것 말고는 알 방법이 없다 |
| `qdrant_operator_backup_snapshots` | 성공했는데 담은 것이 없는 백업 — 성공으로 보이는 실패 |
| `qdrant_operator_dead_replicas` | 내구성이 깎였고 재복제가 걷어내지 못하는 중 |
| `qdrant_operator_planned_moves` | 재배치가 수렴하지 못하는 중 |
| `qdrant_operator_peers` | peer 하나가 합의로 돌아오지 못했다 |
| `qdrant_operator_upgrade_in_progress` | 롤아웃이 건강 게이트에 걸려 있다. 멈춘 업그레이드는 실패하지 않아 다른 무엇도 알려주지 않는다 |
| `qdrant_operator_shard_operations_total` / `_failures_total` | 발행량과 그 뒤의 실패율 |

`config/prometheus/alerts.yaml` 에 각 지표에 대응하는 `PrometheusRule` 이 있고, 임계값마다 그 근거를 옆에 적어 뒀다. 규칙 없는 지표는 아무도 안 보는 대시보드에 남고, 그 상태는 지표가 없는 것과 구별되지 않는다.

CR 이 사라지면 그 시계열도 걷어낸다 — 남겨두면 삭제된 클러스터의 마지막 값이 고정돼 영원히 알럿을 울린다.

## 정직한 한계 (반드시 읽어주세요)

이 오퍼레이터는 "새 기능"보다 "파괴적 실수를 구조적으로 막는 것"에 무게를 둔다.

1. **immutable 필드 변경은 지원되지 않는다.** StatefulSet의 immutable 필드(`serviceName` / `volumeClaimTemplates` / `selector`, 예: `persistence.size`)를 건드리는 spec 변경은 crash-loop patch를 시도하는 대신 `Degraded` condition + Event로 표면화되며 StatefulSet은 그대로 보존된다. 제어된 recreate는 Phase D 과제다.
2. **컬렉션을 재생성하지 않는다.** `QdrantCollection` spec이 라이브 컬렉션과 vector size · distance · replication factor에서 어긋나면 `ParamsMismatch`로 보고하고 멈춘다. 비파괴 이행 경로가 있는 것은 `shardNumber`뿐이다(alias re-shard).
3. **성한 잉여 복제본은 지우지 않는다.** `replication_factor`를 초과하는 replica는 관측·보고만 하고 자동 드롭하지 않는다. 죽은 사본만이 예외다 — 서빙에 쓰이지 않고, 성한 사본이 복제 계수를 충족한 뒤에만 회수된다.
4. **S3 에서 복원하려면 위치를 명시해야 한다.** qdrant 는 URL 또는 로컬 파일에서 복원하며 `s3://` 위치를 받지 않는다. 그리고 스냅샷이 버킷에 있으면 peer 가 그것을 HTTP 로 서빙하지 않는다. 따라서 `fromBackup` 은 peer 로컬 스냅샷에만 쓸 수 있고, S3 보관이면 `spec.sources` 에 peer 별 주소를 준다. 오퍼레이터는 존재하지 않는 URL 로 복원을 발행하는 대신 주소를 모른다고 말하고 멈춘다.
5. **크기 기반 배치는 전 peer 관측을 요구한다.** 2차 균형 기준이 되는 peer별 points는 peer를 하나씩 돌며 모은다. 하나라도 응답하지 않으면 부분 지도로 판단하는 대신 크기 단계 전체를 건너뛴다.

데이터 안전을 위해 PVC(`volumeClaimTemplates`)는 오퍼레이터가 **의도적으로 소유하지 않는다** — `QdrantCluster`를 삭제해도 PVC는 남는다(`persistence.retentionPolicy: Delete`를 설정한 경우에만 회수된다).

## 설치

오퍼레이터는 자체 Helm 차트로 배포한다.

```sh
helm install qdrant-operator ./deploy/chart \
  --namespace qdrant-operator-system --create-namespace
```

CRD, RBAC, leader-election이 활성화된 controller-manager Deployment가 함께 설치된다. 컨테이너 이미지는 `ghcr.io/keiailab/qdrant-operator`에 게시된다.

### 소스에서 설치

```sh
make install                                   # CRD 설치
make deploy IMG=ghcr.io/keiailab/qdrant-operator:latest
```

## 사용법

```yaml
apiVersion: qdrant.keiailab.com/v1alpha1
kind: QdrantCluster
metadata:
  name: my-qdrant
  namespace: data
spec:
  image:
    repository: qdrant/qdrant   # default
    tag: v1.18.2                # default
  replicas: 3
  resources:
    requests: { cpu: 250m, memory: 512Mi }
    limits:   { cpu: "2",  memory: 4Gi }
  persistence:
    size: 10Gi                   # default
    storageClassName: ceph-rbd   # default
    accessModes: [ReadWriteOnce] # default
    retentionPolicy: Retain      # default — Retain | Delete
  config:
    clusterEnabled: true   # default — 분산(Raft) 모드
    tlsEnabled: false
    # rawOverride: {}      # production.yaml escape hatch (드문 upstream 옵션용)
  serviceType: ClusterIP   # default
  apiKey:
    name: my-qdrant-api-key  # Secret 이름 (필수)
    key: api-key              # default
  runAsUser: 1000  # default
  fsGroup: 3000    # default
```

적용 후 상태 확인:

```sh
kubectl apply -f qdrantcluster.yaml
kubectl get qdrantcluster my-qdrant -n data -o jsonpath='{.status.phase}'
```

## 로드맵

| Phase | 서브시스템 | 핵심 CRD | 무엇을 | 의존 | 상태 |
|---|---|---|---|---|---|
| **A** | 오퍼레이터 기반 + 프로비저닝 | `QdrantCluster` | scaffold · 컨트롤러 · RBAC + 선언적 분산 클러스터 기동 | — | **완료** |
| **B** | 컬렉션 / shard 오케스트레이션 | `QdrantCollection` | 선언적 컬렉션 + auto-rebalance(관측 → 계획 → `move_shard`) + 복제 계수 수리 + alias re-shard + 안전한 scale-in drain | A | **완료** |
| **C** | 데이터 보호 | `QdrantBackup` / `QdrantRestore` | 전 peer snapshot API 스케줄 백업 · S3 오브젝트 스토리지 · 보존기간 · 복원 | A | **완료** |
| **D** | Day-2 / 업그레이드 | (status / metrics) | Raft-aware 롤링 업그레이드 · health gate · observability · TLS | A | **TLS 만 남음** |
| **E** | 오토스케일링 통합 | (`/scale` subresource) | 스케일 트리거 → Phase B의 rebalance 머신에 연결 | B | **완료** — `QdrantCluster`를 KEDA·HPA가 직접 스케일한다. 전용 CRD는 불필요했다 |

의존 그래프: `A → {B, C, D}`는 병렬 진행 가능하고, `E`는 `B` 완료가 필요하다. Phase B가 이 프로젝트의 핵심 가치(shard 재배치 자동화)다.

Phase E는 `QdrantAutoscaler` CRD가 아니라 `/scale` subresource로 착지했다. KEDA는 `/scale`을 노출하는 custom resource라면 무엇이든 스케일할 수 있어, 별도 오토스케일러는 재발명이 된다. KEDA가 결정하고 오퍼레이터가 실행한다.

## API

- Group / Version: `qdrant.keiailab.com/v1alpha1`
- Kind: `QdrantCluster`, `QdrantCollection`
- Domain: `keiailab.com` (`kubebuilder init --domain keiailab.com --group qdrant`)

API는 `v1alpha1`이며, stable 릴리스 이전까지 변경될 수 있다.

## 문서

- 개발 가이드(스캐폴드 구조 · 재생성 명령 · 컨트롤러 컨벤션): [`AGENTS.md`](AGENTS.md)
- 설계 문서(Phase B 이후): [`docs/design/`](docs/design/)

## 릴리스

메인테이너는 GitHub 태그 · ghcr 컨테이너 이미지 · ghcr OCI chart · 중앙 카탈로그 네 채널에 단일 명령으로 동시 발행한다 — 수동 절차는 하나를 빠뜨리기 쉽기 때문이다.

```bash
make release VERSION=0.9.0     # 게이트 → 태그 → 이미지 → chart → 카탈로그 → 검증
DRY_RUN=1 hack/release.sh 0.9.0  # 발행 없이 전체 단계만 출력
make verify-publish            # 현재 상태의 4채널 일치 여부 검사
```

릴리스 게이트는 `test` · `lint` · `publish-scan`을 먼저 통과시키고 `verify-publish`로
결과를 재확인한다 — 하나라도 실패하면 릴리스는 중단된다.

## 기여하기

기여를 환영한다. 사소하지 않은 변경이라면 API 표면에 대해 먼저 합의할 수 있도록 이슈를 먼저 열어달라. [CONTRIBUTING.md](.github/CONTRIBUTING.md)를 참고하고, 전체 빌드 타겟 목록은 `make help`로 확인한다.

보안 이슈는 공개 이슈 대신 [SECURITY.md](.github/SECURITY.md) 절차를 따라 신고한다.

## 라이선스

[MIT](LICENSE) © keiailab

---

<p align="center">© 2026 keiailab · MIT · <a href="https://keiailab.com">keiailab.com</a></p>
