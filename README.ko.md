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

이 오퍼레이터는 그 조립과 재배치를 Kubernetes 컨트롤러로 자동화한다. 스코프가 커서 5개의 순차 Phase로 나누어 개발한다(아래 로드맵 참고).

## 커스텀 리소스

| Kind | 상태 | 하는 일 |
|---|---|---|
| `QdrantCluster` | Phase A (구현됨) | 단일 인스턴스 또는 분산(Raft) Qdrant 클러스터 |
| `QdrantCollection` | Phase B (예정) | shard 재배치를 포함한 선언적 컬렉션 |

모든 리소스는 API 그룹 `qdrant.keiailab.com/v1alpha1`을 사용한다.

## Phase A 스코프 (현재)

이 저장소는 현재 **Phase A: 오퍼레이터 기반 + 프로비저닝**만 구현한다.

**In scope**

- `QdrantCluster` CRD를 통한 분산 Qdrant 클러스터의 선언적 생성/수정/삭제
- 기존 helm 차트 산출물과의 동등성(parity) — ServiceAccount, ConfigMap, headless Service, client Service, StatefulSet
- status 보고 (`phase` / `readyReplicas` / `peers` / `conditions`)
- **scale-up** (naive replica 증가 — 새 peer는 Raft에 join)

**Out of scope (후속 Phase로 이관)**

- 컬렉션 / shard rebalance → Phase B
- 백업 / 복원 → Phase C
- Raft-aware 롤링 업그레이드 오케스트레이션 → Phase D
- 오토스케일링 → Phase E

## 정직한 한계 (반드시 읽어주세요)

Phase A는 "새 기능"보다 "파괴적 실수를 구조적으로 막는 것"에 무게를 둔 최소 프로비저닝 계층이다. 다음 3가지는 **의도적 미지원**이며, 발생 시 StatefulSet을 직접 건드리는 대신 `Degraded` condition + Event로 안전하게 표면화된다.

1. **새로 scale-up된 peer는 빈 상태로 남는다.** `spec.replicas`를 늘리면 새 StatefulSet pod가 Raft에 join하지만, 기존 shard 데이터가 자동으로 옮겨지지는 않는다(OSS Qdrant에는 auto-resharding이 없다). 새 peer는 컬렉션이 명시적으로 재생성되거나 replicate되기 전까지 사실상 빈 노드다. 이 한계는 **Phase B**(컬렉션 / shard 오케스트레이션)에서 해소된다.
2. **scale-down은 거부된다.** 분산 데이터베이스에서 naive한 replica 감소는 최고 서수(highest-ordinal) peer와 그 shard를 유실시킨다. `spec.replicas`를 현재 값보다 낮추는 시도는 `Degraded` condition으로 거부되며 StatefulSet은 변경되지 않는다. 안전한 drain 기반 scale-down은 Phase B 전까지 지원하지 않는다.
3. **immutable 필드 변경은 지원되지 않는다.** StatefulSet의 immutable 필드(`serviceName` / `volumeClaimTemplates` / `selector`, 예: `persistence.size`)를 건드리는 spec 변경은 crash-loop patch를 시도하는 대신 `Degraded` condition + Event로 표면화되며 StatefulSet은 그대로 보존된다. 제어된 recreate는 Phase D 이후 과제다.

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
| **A** | 오퍼레이터 기반 + 프로비저닝 | `QdrantCluster` | scaffold · 컨트롤러 · RBAC + 선언적 분산 클러스터 기동 | — | 진행 중 (본 저장소) |
| **B** | 컬렉션 / shard 오케스트레이션 | `QdrantCollection` | 선언적 컬렉션 + auto-rebalance(관측 → 계획 → `move_shard`) + alias re-shard + 안전한 scale-in drain | A | 예정 |
| **C** | 데이터 보호 | `QdrantBackup` / `QdrantRestore` | snapshot API 스케줄 백업 · 오브젝트 스토리지 · 복원 | A | 예정 |
| **D** | Day-2 / 업그레이드 | (status / webhook) | Raft-aware 무중단 롤링 업그레이드 · health gate · observability · TLS | A | 예정 |
| **E** | 오토스케일링 통합 | `QdrantAutoscaler` | 스케일 트리거 → Phase B의 rebalance 머신에 연결 | B | 예정 |

의존 그래프: `A → {B, C, D}`는 병렬 진행 가능하고, `E`는 `B` 완료가 필요하다. Phase B가 이 프로젝트의 핵심 가치(shard 재배치 자동화)이지만, 오퍼레이터가 클러스터를 소유하는 Phase A가 반드시 선행해야 한다.

## API

- Group / Version: `qdrant.keiailab.com/v1alpha1`
- Kind: `QdrantCluster` (Phase A), `QdrantCollection` (Phase B, 스캐폴딩됨)
- Domain: `keiailab.com` (`kubebuilder init --domain keiailab.com --group qdrant`)

API는 `v1alpha1`이며, stable 릴리스 이전까지 변경될 수 있다.

## 문서

- 개발 가이드(스캐폴드 구조 · 재생성 명령 · 컨트롤러 컨벤션): [`AGENTS.md`](AGENTS.md)
- 설계 문서(Phase B 이후): [`docs/design/`](docs/design/)

## 릴리스

메인테이너는 GitHub 태그 · 컨테이너 이미지 · ghcr OCI chart · 중앙 카탈로그 네 채널에 단일 명령으로 동시 발행한다 — 수동 절차는 하나를 빠뜨리기 쉽기 때문이다.

```bash
make release VERSION=0.7.0     # 게이트 → 태그 → 이미지 → chart → 카탈로그 → 검증
DRY_RUN=1 hack/release.sh 0.7.0  # 발행 없이 전체 단계만 출력
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
