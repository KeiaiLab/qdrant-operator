# qdrant-operator

Kubernetes 위에서 [Qdrant](https://qdrant.tech) 분산 벡터 데이터베이스 클러스터를 `QdrantCluster` CRD 하나로 선언적으로 프로비저닝·운영하는 순수 오픈소스(OSS) 오퍼레이터.

## 왜 필요한가

self-hosted Qdrant를 Kubernetes에서 운영하려면 StatefulSet·Service·ConfigMap·PVC를 직접 조립해야 하고, 노드를 늘리거나 줄일 때 shard 재배치를 수동으로 수행해야 한다. Qdrant는 이에 필요한 프리미티브(Raft peer join · `move_shard` · `replicate_shard` · collection alias)를 모두 공개 API로 제공하지만, 이를 엮어 원하는 상태로 수렴시키는 컨트롤 루프는 운영자 몫으로 남는다.

이 오퍼레이터는 그 조립과 재배치를 Kubernetes 컨트롤러로 자동화하는 것을 목표로 한다. 스코프가 커서 5개 Phase로 나눠 순차 개발한다(아래 로드맵).

## Phase A 스코프 (현재 구현 범위)

이 저장소는 현재 **Phase A: 오퍼레이터 기반 + 프로비저닝**만 구현한다.

**In scope**
- `QdrantCluster` CRD로 분산 Qdrant 클러스터의 선언적 생성/수정/삭제
- 기존 helm 차트 산출물과 parity(의미 등가) 재현 — ServiceAccount · ConfigMap · headless Service · client Service · StatefulSet 5개 리소스
- status 보고 (`phase` / `readyReplicas` / `peers` / `conditions`)
- **scale-up** (naive replica 증가 — 새 peer는 Raft join)

**Out of scope (후속 Phase로 이관)**
- 컬렉션/shard rebalance → Phase B
- 백업/복원 → Phase C
- Raft-aware 롤링 업그레이드 오케스트레이션 → Phase D
- 오토스케일링 → Phase E

## 정직한 한계 (반드시 읽어주세요)

Phase A는 "새 기능"보다 "파괴적 실수를 구조적으로 막는 것"에 무게를 둔 최소 프로비저닝 계층이다. 다음 3가지는 **의도적 미지원**이며, 발생 시 STS를 건드리는 대신 `Degraded` condition + Event로 안전하게 표면화된다.

1. **scale-up 시 새 peer는 빈 상태로 남는다.** `spec.replicas`를 늘리면 새 StatefulSet pod가 Raft에 join은 하지만, 기존 shard 데이터를 자동으로 옮겨주지는 않는다(OSS Qdrant에는 auto-reshard가 없다). 새 peer는 컬렉션이 명시적으로 재생성/replicate되기 전까지 사실상 빈 노드다. 이 한계는 **Phase B**(컬렉션·shard 오케스트레이션)에서 해소한다.
2. **scale-down은 거부된다.** 분산 DB에서 naive한 replica 감소는 최고 서수 peer와 그 shard를 유실시킨다. `spec.replicas`를 현재 값보다 낮추는 시도는 `Degraded` condition으로 거부되고 StatefulSet은 변경되지 않는다. 안전한 drain 기반 scale-down은 Phase B 도착 전까지 지원하지 않는다.
3. **immutable 필드 변경은 미지원이다.** `persistence.size` 등 StatefulSet의 immutable 필드(`serviceName` / `volumeClaimTemplates` / `selector`)를 건드리는 spec 변경은 crash-loop patch 시도 대신 `Degraded` condition + Event로 표면화되고 STS는 그대로 유지된다. controlled recreate는 Phase D 이후 과제다.

데이터 안전을 위해 PVC(`volumeClaimTemplates`)는 오퍼레이터가 **의도적으로 소유하지 않는다** — `QdrantCluster` CR을 삭제해도 PVC는 잔존한다(`persistence.retentionPolicy: Delete`를 명시한 경우에만 회수).

## 설치

오퍼레이터는 자체 Helm 차트로 배포한다.

```sh
helm install qdrant-operator ./deploy/chart \
  --namespace qdrant-operator-system --create-namespace
```

CRD, RBAC, leader-election이 활성화된 controller-manager Deployment가 함께 설치된다.

## QdrantCluster 샘플

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

적용 및 상태 확인:

```sh
kubectl apply -f qdrantcluster.yaml
kubectl get qdrantcluster my-qdrant -n data -o jsonpath='{.status.phase}'
```

## 5-Phase 로드맵

| Phase | 서브시스템 | 핵심 CRD | 무엇을 | 의존 | 상태 |
|---|---|---|---|---|---|
| **A** | 오퍼레이터 기반 + 프로비저닝 | `QdrantCluster` | scaffold·컨트롤러·RBAC + 선언적 분산 클러스터 기동(현 helm 차트 대체) | — | 진행 중 (본 저장소) |
| **B** | 컬렉션·shard 오케스트레이션 | `QdrantCollection` | 선언적 컬렉션 + auto-rebalance(관측→계획→`move_shard`) + alias re-shard + scale-in drain 안전 | A | 예정 |
| **C** | 데이터 보호 | `QdrantBackup` / `QdrantRestore` | snapshot API 스케줄 백업 · 오브젝트 스토리지 · 복원 | A | 예정 |
| **D** | Day-2 / 업그레이드 | (status/webhook) | Raft-aware 무중단 롤링 업그레이드 · health gate · observability · TLS | A | 예정 |
| **E** | 오토스케일링 통합 | `QdrantAutoscaler` | 스케일 트리거 → Phase B의 rebalance 머신 연결 | B | 예정 |

의존 그래프: `A → {B, C, D}`는 병렬 가능, `E`는 `B` 완료가 필요. Phase B가 이 프로젝트의 핵심 가치(shard 재배치 자동화)지만, 클러스터를 오퍼레이터가 소유하는 Phase A가 반드시 선행한다.

## API

- Group/Version: `qdrant.keiailab.com/v1alpha1`
- Kind: `QdrantCluster`
- Domain: `keiailab.com` (`kubebuilder init --domain keiailab.com --group qdrant`)

개발 가이드(스캐폴드 구조·재생성 명령·컨트롤러 컨벤션)는 `AGENTS.md` 참조.

## 라이선스

Apache License 2.0.

## 릴리스 (4채널 일관성)

이 오퍼레이터는 네 곳에 동시에 발행된다 — **GitHub 태그 / 컨테이너 이미지 / ghcr OCI
chart / 중앙 카탈로그**(ArtifactHub 크롤 대상). 수동 절차는 반드시 하나를 빠뜨리므로
단일 명령으로 고정한다.

```bash
make release VERSION=0.7.0     # 게이트 → 태그 → 이미지 → chart → 카탈로그 → 검증
make verify-publish            # 현재 상태의 4채널 일치만 검사
DRY_RUN=1 hack/release.sh 0.7.0  # 발행 없이 단계 확인
```

`make release` 는 품질 게이트(test/lint/publish-scan)를 먼저 통과해야 진행하고,
마지막에 `verify-publish` 로 4채널을 재확인한다 — 어느 하나라도 빠지면 실패한다.
