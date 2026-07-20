# Phase B 설계 — 컬렉션 오케스트레이션 + Shard Rebalance

- 상태: 구현 중 (2026-07-20 착수)
- 선행: Phase A (프로비저닝 — `QdrantCluster`로 분산 클러스터 선언 관리, 라이브 채택 완료)

## 목표

self-hosted Qdrant에서 노드 증감 시의 **shard 재배치**와 **컬렉션 수명주기**를 컨트롤러로
자동화한다. Qdrant는 필요한 프리미티브(Raft peer join · shard move/replicate · collection
alias · snapshot)를 모두 공개 API로 제공한다 — 이 오퍼레이터는 그 위의 수렴 루프를 담당한다.

Phase A의 정직한 한계였던 "scale-up 시 새 peer는 빈 상태"와 "scale-down 전면 거부"를
이 Phase가 해소한다.

## 컴포넌트

### 1. Qdrant API 클라이언트 (`internal/qdrant`)

컨트롤러 ↔ Qdrant REST 사이의 얇은 인터페이스. envtest에는 실제 qdrant가 없으므로
인터페이스 + fake 구현으로 컨트롤러 로직을 결정론 검증하고, 실 HTTP 구현은 e2e가 커버한다.

```go
type Client interface {
    ListCollections(ctx) ([]string, error)
    GetCollection(ctx, name) (*CollectionInfo, error)     // params(shard/replication) + points
    EnsureCollection(ctx, name, spec) (created bool, err) // PUT /collections/{name} (멱등)
    ClusterInfo(ctx) (*ClusterInfo, error)                 // GET /cluster — peers
    CollectionCluster(ctx, name) (*CollectionClusterInfo, error) // shard 분포/전송 상태
    MoveShard(ctx, name, shardID, fromPeer, toPeer) error  // POST /collections/{n}/cluster
    UpdateAliases(ctx, actions) error                      // 원자 alias 스왑
    RemovePeer(ctx, peerID, force bool) error              // DELETE /cluster/peer/{id}
}
```

엔드포인트 주소는 클러스터 client Service(`<name>:6333`)를 기본으로 하고, CR 별로
override 가능하게 둔다(테스트/외부 접근용).

### 2. `QdrantCollection` CRD (namespaced)

컬렉션의 선언적 관리 + 재배치의 단위. 같은 네임스페이스의 `QdrantCluster`를 참조한다.

```yaml
apiVersion: qdrant.keiailab.com/v1alpha1
kind: QdrantCollection
metadata:
  name: my-vectors
  namespace: data
spec:
  clusterRef: platform-data-qdrant   # 같은 ns 의 QdrantCluster 이름
  collectionName: ""                 # 기본 = metadata.name
  vectors:                           # 신규 생성 시 스키마 (기존 채택 시 검증만)
    size: 384
    distance: Cosine
  shardNumber: 2                     # 생성 시 고정 — 변경은 re-shard 워크플로(후속 마일스톤)
  replicationFactor: 1
  onDelete: Retain                   # Retain | Delete — CR 삭제 시 컬렉션 처분
status:
  phase: Pending|Ready|Adopted|Degraded
  pointsCount: 0
  shards: { total: 2, perPeer: {"peer-1": 1, "peer-2": 1} }
  conditions: [...]
```

동작 원칙:
- **존재 보장(ensure)**: 없으면 생성(PUT은 멱등), 있으면 **채택** — 단 spec과 실제
  파라미터(shardNumber/replicationFactor/vectors)가 다르면 파괴적 변경 대신
  `Degraded`(ParamsMismatch)로 표면화한다. 컬렉션 재생성은 절대 자동으로 하지 않는다.
- **onDelete=Retain 기본**: CR 삭제가 데이터 삭제로 이어지지 않는다(파이널라이저는
  Delete 명시 시에만 컬렉션 DELETE 호출).

### 3. Shard Rebalancer (`QdrantCluster` 컨트롤러 확장)

관측 → 계획 → 실행의 수렴 루프. Cloud 관리형에만 있던 자동 재배치의 OSS 구현.

- **관측**: `/cluster`(peer 목록) + 컬렉션별 `/collections/{c}/cluster`(shard 위치,
  진행 중 transfer). 결과를 `QdrantCluster.status.shardDistribution`으로 보고.
- **계획**: 목표 = 컬렉션별로 peer 간 shard 수 균형(최대-최소 ≤ 1). 초과 peer →
  부족 peer로의 이동 목록 산출. 계획은 실행 전 `status.plannedMoves`로 먼저 노출된다
  (관측 가능성 — 무슨 이동을 왜 하려는지 status 로 항상 설명).
- **실행**: **동시 1건**의 `move_shard`만 발행하고 완료(transfer 소멸)를 확인한 뒤 다음
  이동으로 넘어간다. 이동은 네트워크/디스크/재색인 비용이 커서 동시 다발은 서비스에
  해롭다. 실패 시 backoff + `Degraded(MoveFailed)`.
- **트리거**: replicas 변경(scale-up 후 새 peer 합류)과 shard 불균형 감지 시에만.
  균형 상태에서는 아무것도 하지 않는다(steady-state 무간섭).

### 4. Scale-in Drain (Phase A 거부 가드의 상향)

Phase A는 scale-down을 전면 거부했다(shard 유실 방지). Phase B는 이를 절차로 대체한다:

1. `spec.replicas` 감소 감지 → 제거 대상 = 최고 서수 peer(들)
2. 대상 peer의 모든 shard를 잔존 peer로 이동(위 실행 루프 재사용)
3. 대상 peer가 빈 것을 확인 → `DELETE /cluster/peer/{id}`로 합의에서 제거
4. 그 후에만 STS replicas를 실제로 낮춘다

드레인이 완료되기 전까지 STS는 축소되지 않으며, 진행 상황은 `status.phase=Draining`으로
보고된다. 이동 불가(예: 잔존 용량 부족) 시 축소를 보류하고 `Degraded`로 알린다.

### 5. Re-shard 워크플로 (후속 마일스톤)

`shardNumber`는 Qdrant에서 생성 후 변경할 수 없다. 변경 요구는 shadow collection
패턴으로 처리한다: `<name>-reshard-<n>` 생성(새 shardNumber) → 데이터 복사(scroll
또는 snapshot) → **alias 원자 스왑** → 구 컬렉션 처분(onDelete 정책). 소비자는
alias로만 접근하므로 무중단이다. 세부 설계는 구현 마일스톤에서 확정한다.

## 마일스톤 (증분 머지 — 각각 독립 검증)

| # | 내용 | 검증 |
|---|---|---|
| B-1 | qdrant client 인터페이스+fake+HTTP 구현 / `QdrantCollection` CRD / ensure·adopt 컨트롤러 | envtest(fake) + 단위 |
| B-2 | shard 분포 관측 → `QdrantCluster.status` 보고 | envtest(fake 분포 주입) |
| B-3 | rebalance 계획+실행(동시 1건, scale-out 통합) | fake 시나리오(불균형→수렴) + e2e |
| B-4 | scale-in drain(거부 가드 대체) | fake + e2e |
| B-5 | alias re-shard 워크플로 | fake + e2e |

## 안전 원칙 (전 마일스톤 공통)

- 파괴적 동작(컬렉션 삭제·peer 제거·재생성)은 명시적 선언 없이는 절대 수행하지 않는다.
- 모든 자동 행위는 실행 전 status로 계획을 노출한다(사후 포렌식이 아니라 사전 관측).
- 이동/드레인은 동시 1건 — 서비스 부하를 상수로 유지.
- 실패는 조용한 재시도 루프가 아니라 `Degraded` 조건 + Event로 표면화한다.
