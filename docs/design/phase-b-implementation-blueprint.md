# Phase B (B-2~B-5) 통합 설계 — 관측·재배치·드레인·리샤드

- 상태: 설계 확정 (단일 이미지 `v0.2.0` 롤아웃 대상)
- 대상 컨트롤러: `QdrantCluster`(관측·재배치·드레인) + `QdrantCollection`(리샤드)
- 선행: Phase A(프로비저닝) + B-1(컬렉션 수명주기) 구현 완료

---

## 1. 개요

Phase A는 정직하게 두 가지를 미뤘다 — scale-up 후 새 peer가 빈 손인 채 남는 것과 scale-down 전면 거부다. Phase B는 이를 관측 → 계획 → 동시 1건 실행의 수렴 루프로 해소한다. 네 서브시스템이 하나의 안전 계약을 공유한다.

### 1.1 공통 안전 계약 (전 서브시스템 불변식)

1. **관측은 항상 안전하다** — 관측은 GET 전용이며 실패해도 reconcile을 막지 않고 클러스터 헬스(StatefulSet readiness)에 영향을 주지 않는다.
2. **선노출** — 모든 자동 변이는 실행 *전에* status로 계획을 노출한다(사후 포렌식이 아니라 사전 관측). status 커밋이 API 서버에 관측 가능해진 *뒤에만* 쓰기 API를 호출한다.
3. **전역 단일 무거운 연산** — 클러스터 전역에서 진행 중인 무거운 데이터 연산(shard 이동/드롭, 리샤드 백필)은 최대 1건. §2.2의 우선순위 규칙으로 강제된다.
4. **비파괴 기본값** — 컬렉션 삭제·peer 제거·재생성·리샤드는 명시 선언 없이는 절대 수행하지 않는다. 마지막 활성 replica를 유실시키는 이동/드롭/축소는 금지.
5. **steady-state 무행동** — 트리거(불균형/replicas 변화/spec 변경/명시 리샤드 opt-in)가 없으면 mutating 연산은 정확히 0이며, 주기 재큐로 인한 status 재기록조차 없다.
6. **표면화** — 실패는 조용한 재시도 루프가 아니라 `Degraded` 조건 + Event로 드러난다.

### 1.2 v0.2.0 비목표 (문서화된 한계)

- **API 키 인증 배선**: 모든 HTTP 클라이언트는 단일 생성자(`NewHTTPClient`)를 공유한다. 키 인증은 이 한 지점의 후속 확장으로 남긴다 — B-2~B-5의 신규 호출은 이 생성자를 재사용하므로 활성화 시 전 경로가 함께 적용된다.
- **named-vectors 스키마**: 단일 벡터 스키마만 파싱한다. named-vectors 컬렉션은 명시적 `Degraded(UnsupportedVectorSchema)`로 표면화한다(조용한 오파싱 금지 — §5.4).
- **복사 중 라이브 쓰기(리샤드)**: v0.2.0 리샤드는 원본이 quiescent(읽기 위주)임을 계약으로 가정하고, point-count 델타 가드로 보수적으로 방어한다(§8.6). 이중쓰기·WAL tail 복사는 후속.

---

## 2. 아키텍처 결정

### 2.1 컨트롤러 토폴로지 — 단일 status writer 원칙

| 관심사 | 소유 컨트롤러 | 근거 |
|---|---|---|
| 관측(B-2) · 재배치(B-3) · 드레인(B-4) | `QdrantClusterReconciler` (스테이지 함수) | 셋 다 `QdrantCluster.status`를 쓴다. 별도 컨트롤러는 `Status().Update` 경합을 일으킨다. 셋이 동일한 관측 스냅샷(`GET /cluster` + 컬렉션별 `GET .../cluster`)을 원자 소비한다. `Owns(StatefulSet)`가 이미 replicas/ready 변화를 reconcile로 깨운다. |
| 리샤드(B-5) | `QdrantCollectionReconciler` (스테이지 확장) | 리샤드는 컬렉션 단위 선언(`spec.shardNumber` 변경)으로 트리거되고 `QdrantCollection.status`를 쓴다. 이미 `QdrantClientFor` 훅으로 대상 클러스터 클라이언트를 주입받는다. |

두 컨트롤러는 **자신의 status만 쓰고 상대의 status는 읽기만** 한다 — 이 읽기 전용 상호 참조가 §2.2 전역 배타의 배선이다(쓰기 경합 0).

### 2.2 전역 단일 무거운 연산 — 강제 지점과 우선순위

무거운 연산은 세 종류다: (a) 재배치 이동, (b) 드레인 이동/드롭, (c) 리샤드 백필. (a)(b)는 같은 컨트롤러의 **통합 실행 lane**(§7)이 `shard_transfers` 관측 + 단일 `status.activeMove` 레코드로 직렬화한다. (c)는 scroll/upsert라 `shard_transfers`에 나타나지 않으므로 **읽기 전용 상호 deference**로 (a)(b)와 배타를 이룬다:

- **클러스터 스테이지(재배치/드레인)는 새 이동을 발행하기 직전** 같은 네임스페이스에서 `clusterRef`가 자신을 가리키는 `QdrantCollection` 중 `status.reshard != nil`(활성 리샤드)이 하나라도 있으면 발행을 **유예**한다(Progressing=`ReshardInFlight`, requeue).
- **리샤드 스테이지는 shadow 생성/복사/스왑을 진행하기 직전** 대상 `QdrantCluster.status.activeMove != nil`(진행 중 shard 이동/드롭)이면 **유예**한다(requeue).

**우선순위는 비대칭이다 — "이미 진행 중인 연산이 이긴다":** 리샤드는 *실제 in-flight 이동*(`activeMove != nil`)에만 양보하고 단순 phase 라벨(`Draining`/`Rebalancing` 대기 상태)에는 양보하지 않는다. 따라서 "드레인이 리샤드를 기다리고 리샤드가 드레인을 기다리는" 교착이 성립하지 않는다: 리샤드가 활성이면 클러스터는 이동 발행만 유예(activeMove는 nil로 유지)하고, 리샤드는 activeMove가 nil이므로 완주한다. 반대로 이동이 이미 발행돼 activeMove가 세팅됐다면, 아직 시작 전인 리샤드가 그 이동에 양보하고 이동이 정산된 뒤 진행한다.

### 2.3 관측-우선 파이프라인 (드레인이 관측을 우회하지 않는다)

드레인이든 정상 경로든 **매 reconcile은 먼저 라이브 관측으로 `status.{replicas, readyReplicas, peers, shardDistribution}`를 갱신한 뒤** 재배치/드레인 스테이지를 실행한다. 드레인 경로도 동일한 `observe()`를 호출하므로 B-2 분포 계약(모든 peer 표기, qdrant 서버가 SSOT)이 드레인 진행 20분 내내 신선하게 유지된다. 관측 로직은 단일 헬퍼(`refreshObserved`)로 두 경로가 공유한다(중복 폴링 금지).

### 2.4 무저장 재계산 (멱등 재개의 근원)

계획은 저장된 프로그램 카운터가 아니라 매 reconcile 라이브 관측에서 재산출된다. 진실의 원천은 세 가지뿐이다 — 물리 크기(`liveSTS.spec.replicas`), 목표 크기(`spec.replicas`), shard 배치(qdrant 관측). `status.{plannedMoves, activeMove, drainStatus, reshard}`는 이 관측의 **보고용 미러**일 뿐 실행 상태 기계가 아니다. 컨트롤러가 이동 중·peer 제거 직후·축소 직전 어디서 크래시해도 재관측으로 남은 계획을 재도출해 중복·꼬임 없이 재개된다.

---

## 3. Qdrant Client 인터페이스 — 최종 확장

B-1/B-2/B-3에서 이미 존재하는 9개 메서드에 B-4/B-5용 4개를 additive로 추가한다(기존 메서드·테스트 불변). `var _ Client = (*Fake)(nil)` / `var _ Client = (*HTTPClient)(nil)` 컴파일타임 단언이 누락을 즉시 빌드 실패시킨다.

```go
type Client interface {
    // B-1 컬렉션 수명주기
    GetCollection(ctx context.Context, name string) (CollectionInfo, error)
    CreateCollection(ctx context.Context, name string, spec CollectionSpec) error
    DeleteCollection(ctx context.Context, name string) error
    // B-2 관측 (전부 GET only)
    ListCollections(ctx context.Context) ([]string, error)          // ★ 정렬 추가(결정론)
    ClusterInfo(ctx context.Context) (*ClusterInfo, error)
    CollectionCluster(ctx context.Context, name string) (*CollectionClusterInfo, error)
    // B-3/B-4 실행
    MoveShard(ctx context.Context, collection string, shardID uint32, from, to uint64) error
    RemovePeer(ctx context.Context, peerID uint64, force bool) error
    // ── B-4 신규 ──
    // DropReplica 는 shard 의 특정 peer 상 잉여 복제본 1개를 제거한다(POST .../cluster drop_replica).
    // 드레인에서 대상 peer 의 shard 를 옮길 keeper 가 없을 때(모든 keeper 가 이미 그 shard 보유, RF>1)만
    // 호출한다. 안전 계약: keeper 에 활성 복제본이 최소 1개 남을 때만 호출(마지막 복제본 드롭 금지 —
    // qdrant 도 거부). RF=1 경로에서는 절대 발화하지 않는다(항상 MoveShard).
    DropReplica(ctx context.Context, collection string, shardID uint32, peerID uint64) error
    // ── B-5 신규 ──
    // ScrollPoints 는 point 를 배치로 읽는다. point id 는 정수(uint64) 또는 UUID 문자열 두 표현이 있고
    // 커서로 같은 형태로 되돌아오므로, id 를 해석하지 않고 raw JSON 으로 왕복시켜 정밀도 손실을 원천
    // 차단한다. cursor="" 면 처음부터. nextCursor="" 면 마지막 페이지.
    ScrollPoints(ctx context.Context, collection, cursor string, limit int) (points []json.RawMessage, nextCursor string, err error)
    // UpsertPoints 는 raw point 를 shadow 에 멱등(point-id 기준) 재삽입한다. wait=true 동기.
    UpsertPoints(ctx context.Context, collection string, points []json.RawMessage) error
    // ListAliases 는 alias→collection 매핑을 반환한다(스왑 전 확인/스왑 후 커밋 검증).
    ListAliases(ctx context.Context) (map[string]string, error)
    // B-5 원자 alias 스왑 (기존)
    UpdateAliases(ctx context.Context, actions []AliasAction) error
}
```

HTTP 매핑(1.18 실측 스키마): `POST /collections/{c}/cluster {"drop_replica":{"shard_id","peer_id"}}` · `POST /collections/{s}/points/scroll {"limit","offset","with_payload":true,"with_vector":true}` → `{result:{points:[…],next_page_offset}}` · `PUT /collections/{shadow}/points?wait=true {"points":[…]}` · `GET /aliases` → `{result:{aliases:[{alias_name,collection_name}]}}`.

> **B-2 하드닝(§5.1)**: `HTTPClient.ListCollections`에 `slices.Sort(names)` 한 줄을 추가한다. `Fake.ListCollections`는 이미 정렬하므로, 이 추가로 두 구현의 순회 순서가 결정론으로 통일되어 컬렉션 순서 흔들림으로 인한 status 재기록 self-trigger가 제거된다.

---

## 4. CRD 스키마 — 최종 확장

### 4.1 `QdrantCluster` (spec 무변경, status 확장)

spec에는 **새 필드가 없다** — 재배치는 기존 `spec.rebalance.enabled`(dry-run 토글)만 쓰고, 드레인은 활성 기본 동작이며 `spec.replicas` 감소가 유일 트리거다(별도 opt-in 없음).

```go
// ── 통합 실행 lane 레코드 (B-3 재배치 + B-4 드레인 공유) ──
// 이전 RebalanceMoveStatus 를 대체·일반화한다. 한 시점에 "발행돼 추적 중인 단일 무거운 연산"을
// 표현하며, 재배치 스테이지와 드레인 스테이지가 같은 이 필드를 읽고 쓴다(단일 lane).
type MoveStatus struct {
    Kind       string       `json:"kind"`       // "Rebalance" | "Drain"
    Collection string       `json:"collection"`
    ShardID    int32        `json:"shardId"`
    FromPeer   string       `json:"fromPeer"`   // 십진 문자열(peer id 는 uint64 전 범위)
    ToPeer     string       `json:"toPeer,omitempty"` // Drop 이면 무의미
    Drop       bool         `json:"drop,omitempty"`   // true = drop_replica, false = move_shard
    IssuedAt   *metav1.Time `json:"issuedAt,omitempty"`
}

// DrainStatus 는 진행 중 scale-in drain 의 계획+진척(보고용 미러, 실행 카운터 아님).
type DrainStatus struct {
    TargetReplicas  int32       `json:"targetReplicas"`
    CurrentReplicas int32       `json:"currentReplicas"`
    Peers           []string    `json:"peers,omitempty"`        // 비워서 제거할 대상(최고 서수 우선)
    PendingMoves    []string    `json:"pendingMoves,omitempty"` // "coll/shard: from->to" | "coll/shard: drop@peer"
    Message         string      `json:"message,omitempty"`
    StartedAt       metav1.Time `json:"startedAt,omitempty"`
}

type QdrantClusterStatus struct {
    Phase              string   `json:"phase,omitempty"` // Provisioning|Running|Rebalancing|Draining
    Replicas           int32    `json:"replicas,omitempty"`
    ReadyReplicas      int32    `json:"readyReplicas,omitempty"`
    Peers              []string `json:"peers,omitempty"`
    ObservedGeneration int64    `json:"observedGeneration,omitempty"`

    // +optional
    ShardDistribution []CollectionDistribution `json:"shardDistribution,omitempty"` // B-2
    // +optional
    PlannedMoves []string `json:"plannedMoves,omitempty"` // B-3 재배치 후보 상위 10 선노출
    // 통합 lane — 발행 중 이동/드롭 1건. nil = 발행 중 없음. (구 Rebalance 필드 대체)
    // +optional
    ActiveMove *MoveStatus `json:"activeMove,omitempty"`
    // 이동/드롭 발행 실패·유실의 연속 횟수 — 백오프 입력. 완료·균형 도달 시 0.
    // (레코드가 아니라 클러스터 status 의 스칼라라 lane 정산으로 레코드를 비워도 유지된다.)
    // +optional
    MoveBackoff int32 `json:"moveBackoff,omitempty"`
    // B-4 scale-in drain 계획+진척. nil = 드레인 없음. 정상(비-축소) 경로 진입 시 nil 로 정리.
    // +optional
    DrainStatus *DrainStatus `json:"drainStatus,omitempty"`

    // +patchMergeKey=type +patchStrategy=merge +listType=map +listMapKey=type +optional
    Conditions []metav1.Condition `json:"conditions,omitempty"`
}
```

> 참고: 기존 코드에서 `QdrantClusterStatus`의 doc 주석이 `RebalanceSpec` 타입 위에 오배치돼 있어 생성 CRD의 `perPeer` description이 부정확하다. 본 확장 시 doc 주석을 올바른 타입 위로 정리한다(기능 영향 없음, 스키마 정합).

### 4.2 `QdrantCollection` (spec/status 확장 — 리샤드 게이트)

```go
type QdrantCollectionSpec struct {
    ClusterRef        string      `json:"clusterRef"`
    CollectionName    string      `json:"collectionName,omitempty"`
    Vectors           VectorsSpec `json:"vectors"`
    // ★ 포인터화 — nil 이면 라이브 shardNumber 를 채택(리샤드 트리거 아님). 명시 설정 시에만 목표가
    //   된다. PersistenceSpec.Size 의 포인터 선례와 동일한 "0/기본값 구분 불가" 해소.
    //   생성(신규) 시 nil → 1. 채택(존재) 시 nil → 라이브 값(불일치 없음).
    // +kubebuilder:validation:Minimum=1
    // +optional
    ShardNumber       *uint32 `json:"shardNumber,omitempty"`
    // +kubebuilder:default=1
    // +kubebuilder:validation:Minimum=1
    ReplicationFactor uint32 `json:"replicationFactor,omitempty"`
    OnDelete          CollectionDeletePolicy `json:"onDelete,omitempty"` // 기본 Retain

    // Alias 는 소비자가 접근하는 논리 이름. 설정 시 컨트롤러가 이 alias 를 항상 라이브 물리
    // 컬렉션에 정렬한다. 무중단 리샤드 스왑의 전제 — alias 없이는 shardNumber 변경이 리샤드가
    // 아니라 Degraded(ReshardRequired)로만 표면화된다.
    // +optional
    Alias string `json:"alias,omitempty"`
    // Reshard 는 shardNumber 단독 상이 시 처리 정책. onDelete=Retain 과 같은 안전 기본값 패턴
    // (파괴적/고비용 동작 명시 opt-in). 기능을 끄는 플래그가 아니다.
    // +kubebuilder:validation:Enum=Manual;Auto
    // +kubebuilder:default="Manual"
    Reshard ReshardPolicy `json:"reshard,omitempty"`
}

type ReshardPolicy string
const ( ReshardManual ReshardPolicy = "Manual"; ReshardAuto ReshardPolicy = "Auto" )

// ReshardStatus — 워크플로 세부 진행(실행 전 계획 선노출).
type ReshardStatus struct {
    Phase             string       `json:"phase"` // Preparing|Copying|Swapping|Finalizing|Failed|Blocked
    SourceCollection  string       `json:"sourceCollection"`
    ShadowCollection  string       `json:"shadowCollection"` // <physical>-rs-g<generation>, 시작 시 확정·고정(SSOT)
    TargetShardNumber uint32       `json:"targetShardNumber"`
    CopiedPoints      uint64       `json:"copiedPoints"`
    TotalPoints       uint64       `json:"totalPoints"`
    Cursor            string       `json:"cursor,omitempty"`  // scroll next_page_offset(JSON 인코딩)
    StartedAt         *metav1.Time `json:"startedAt,omitempty"`
    Attempts          int32        `json:"attempts,omitempty"`
}

type QdrantCollectionStatus struct {
    Phase              string `json:"phase,omitempty"` // Pending|Ready|Degraded|Resharding
    Adopted            bool   `json:"adopted,omitempty"`
    PointsCount        uint64 `json:"pointsCount,omitempty"`
    ObservedGeneration int64  `json:"observedGeneration,omitempty"`
    // ActiveCollection 은 이 CR 을 현재 뒷받침하는 물리 컬렉션(alias 타깃). 리샤드 성공 스왑마다
    // 새 shadow 이름으로 갱신. 빈 값이면 TargetCollectionName() 과 동일.
    // +optional
    ActiveCollection string `json:"activeCollection,omitempty"`
    // +optional
    Reshard *ReshardStatus `json:"reshard,omitempty"`
    // +listType=map +listMapKey=type +optional
    Conditions []metav1.Condition `json:"conditions,omitempty"`
}
```

---

## 5. B-2 — Shard 분포 관측 (하드닝)

B-2 관측 로직(`observe()`: `GET /cluster` → `GET /collections` → 컬렉션별 `GET .../cluster`, peer별 shard 수 집계, 0-shard peer 포함 표기)은 기존 계약 그대로다. steady-state 무행동 계약을 실제로 달성하기 위해 **self-trigger 재기록 3중 방어**를 추가한다.

### 5.1 결정론 순서

`HTTPClient.ListCollections`에 `slices.Sort(names)` 추가(§3). 컬렉션 순회 순서가 매 cycle 동일해져 `status.shardDistribution` 배열이 안정된다.

### 5.2 status 변경 감지 커밋

`reconcileStatus`/`reconcileDrainCycle`은 무조건 `Status().Update`하지 않는다. 진입 시 `before := qc.Status.DeepCopy()`, 종료 시 `apiequality.Semantic.DeepEqual(before, &qc.Status)`가 아닐 때만 커밋한다. `meta.SetStatusCondition`은 status가 실제로 바뀔 때만 `LastTransitionTime`을 갱신하므로 안정 조건은 churn하지 않는다.

### 5.3 watch predicate

`SetupWithManager`의 `For(&QdrantCluster{})`에 `builder.WithPredicates(predicate.GenerationChangedPredicate{})`를 부착한다. 자신의 status 쓰기(generation 불변)로 인한 재-reconcile watch 이벤트를 걸러 self-trigger 루프를 근원 차단한다. 다단계 진행은 전부 `RequeueAfter`(watch 이벤트가 아니라 명시 재큐)로 구동되고 `Owns(StatefulSet)` 워치(readiness 재점화)는 별도 워치라 predicate에 영향받지 않으므로 안전하다.

### 5.4 named-vectors 방어

`HTTPClient.GetCollection`이 named-vectors(맵 스키마) 컬렉션을 만나면 조용히 `VectorSize=0`으로 오파싱하는 대신 명시적 에러를 반환한다 → 컨트롤러가 `Degraded(UnsupportedVectorSchema)`로 표면화(리샤드 게이트 오판 예방).

---

## 6. B-3 — Shard 재배치

### 6.1 순수 planner (불변)

`planRebalance(obs)`는 순수 함수다. 컬렉션마다 peer별 shard 수를 세고(0-shard peer 포함), `count[donor]-count[recipient] >= 2`이며 recipient가 그 shard replica를 아직 안 가진(distinct-peer) 최소 shard를 후보로 열거해 결정론 총순서(donor count↓ → recipient count↑ → donor id↓ → recipient id↑ → shard id↑ → 컬렉션명↑)로 정렬한다. `>=2` 게이트가 매 이동을 `Σcount²` 엄격 감소시켜 유한 종료 + 종료 시 컬렉션별 max-min≤1을 보장한다. **relocation은 `move_shard`만 사용**(타깃 Active 후 소스 삭제 → 전 구간 RF 유지). transfer method는 `stream_records` 고정(신규 배치 대상이라 `wal_delta` 부적합, `snapshot`은 양쪽 추가 디스크 필요).

### 6.2 executor — 통합 lane 사용

executor는 §7 통합 실행 lane을 쓴다. ready 게이트(STS ReadyReplicas==spec.replicas && 관측 성공 && 관측 raft peer 수==spec.replicas) 통과 후에만 발행하며, scale 직후 peer 합류 미완은 `PeersJoining`으로 15s 대기(아직 없는 peer로 이동 금지). 발행 전 리샤드 활성 여부를 확인해 유예(§2.2).

---

## 7. 통합 실행 lane — B-3·B-4 공유 (핵심)

재배치와 드레인은 "발행돼 추적 중인 단일 무거운 연산"을 **하나의 `status.activeMove` 레코드**로 공유한다. 두 스테이지가 발행 전 반드시 통과하는 공유 정산 함수 `settleActiveMove`가 완료/출현창/유실을 판정한다 — B-3·B-4가 서로의 진행을 인지하지 못해 발생하던 허위 실패를 원천 제거한다.

```go
const (
    moveAppearDeadline = 60 * time.Second // 발행 후 관측에 나타나야 하는 기한
    moveBaseBackoff    = 30 * time.Second
    moveMaxBackoff     = 10 * time.Minute
    moveMaxBackoffShift = 5 // 30s<<5=16m > 10m 상한 → 이 이상은 무의미, overflow 방지 clamp
)

// backoff 는 유실/실패 연속 횟수 level 에 대한 재큐 지연 — 지수이되 상한·비음수 보장.
func backoff(level int32) time.Duration {
    exp := level - 1
    if exp < 0 { exp = 0 }
    if exp > moveMaxBackoffShift { exp = moveMaxBackoffShift }
    d := moveBaseBackoff << uint(exp)
    if d <= 0 || d > moveMaxBackoff { d = moveMaxBackoff } // int64 wrap 방어 + 상한
    return d
}

// activeMoveCompleted 는 move/drop 각각의 완료를 관측 기반으로 판정(shard_transfers 반영 여부와 무관).
func activeMoveCompleted(obs *observation, am *MoveStatus) bool {
    cc, ok := obs.Collections[am.Collection]
    if !ok { return false }
    from, _ := strconv.ParseUint(am.FromPeer, 10, 64)
    if am.Drop { // 드롭 완료 = source 가 더는 그 shard 를 보유하지 않음
        for _, s := range cc.Shards { if int32(s.ShardID)==am.ShardID && s.PeerID==from { return false } }
        return true
    }
    to, _ := strconv.ParseUint(am.ToPeer, 10, 64)
    var toActive, fromHolds bool
    for _, s := range cc.Shards {
        if int32(s.ShardID) != am.ShardID { continue }
        if s.PeerID==to && s.State==shardStateActive { toActive = true }
        if s.PeerID==from { fromHolds = true }
    }
    return toActive && !fromHolds
}

// settleActiveMove — 두 스테이지 공통 진입. 반환 state 로 호출자가 분기한다.
//   busy    : 전역 transfer in-flight → 새 발행 금지, 폴링
//   waiting : 발행 후 출현창 내 미관측 → 대기
//   lost    : 유실 판정 → 레코드 비움 + 백오프(재계획 허용) — 여기서 stall 을 끊는다
//   settled : 완료 정산 또는 추적 없음 → 호출자가 계획 단계로 진행
func (r *QdrantClusterReconciler) settleActiveMove(ctx context.Context, qc *QdrantCluster, obs *observation) (state string, requeue time.Duration) {
    if obs.transfersInFlight() > 0 { return "busy", 10*time.Second }
    am := qc.Status.ActiveMove
    if am == nil { return "settled", 0 }
    switch {
    case activeMoveCompleted(obs, am):
        qc.Status.ActiveMove = nil
        qc.Status.MoveBackoff = 0
        meta.SetStatusCondition(&qc.Status.Conditions, cond(condDegraded, False, "MoveCompleted", …))
        return "settled", 0
    case am.IssuedAt != nil && time.Since(am.IssuedAt.Time) < moveAppearDeadline:
        return "waiting", 5*time.Second
    default: // lost-command
        qc.Status.ActiveMove = nil     // ★ 레코드를 비워 다음 cycle 이 반드시 재계획에 도달(영구 정지 차단)
        qc.Status.MoveBackoff++        // ★ 백오프 카운터는 스칼라라 레코드 비움과 무관하게 escalate
        d := backoff(qc.Status.MoveBackoff)
        meta.SetStatusCondition(&qc.Status.Conditions, cond(condDegraded, True, "MoveFailed", …lost…))
        r.Recorder.Event(qc, Warning, "MoveFailed", "이동/드롭 명령 유실 — 재계획 예정")
        return "lost", d
    }
}

// issueActiveMove — 선노출 후 단일 발행(move 또는 drop). 즉시 실패도 유실과 동일 회복.
func (r *QdrantClusterReconciler) issueActiveMove(ctx, qc, m plannedMove, kind string, qcl) (requeue time.Duration, err error) {
    now := metav1.Now()
    qc.Status.ActiveMove = &MoveStatus{Kind:kind, Collection:m.Collection, ShardID:int32(m.ShardID),
        FromPeer:fmt(m.From), ToPeer:fmt(m.To), Drop:m.Drop, IssuedAt:&now}
    if e := r.Status().Update(ctx, qc); e != nil { return 5*time.Second, e } // 선노출: 발행 전 커밋
    if m.Drop { err = qcl.DropReplica(ctx, m.Collection, m.ShardID, m.From) } else { err = qcl.MoveShard(ctx, m.Collection, m.ShardID, m.From, m.To) }
    if err != nil {
        qc.Status.ActiveMove = nil; qc.Status.MoveBackoff++            // 즉시 실패도 레코드 비움(stall 차단)
        meta.SetStatusCondition(&qc.Status.Conditions, cond(condDegraded, True, "MoveFailed", err.Error()))
        r.Recorder.Event(qc, Warning, "MoveFailed", err.Error())
        return backoff(qc.Status.MoveBackoff), err
    }
    r.Recorder.Event(qc, Normal, eventReason(kind, m.Drop), m.String())
    return 10*time.Second, nil
}
```

**정산의 핵심**: lost/즉시실패 시 `activeMove`를 **nil로 비운다**. 그래야 다음 cycle에 `am==nil → settled`로 나와 계획 단계에 도달한다(구 코드는 `IssuedAt=nil`만 지워 레코드가 non-nil로 남아 매 cycle lost 분기로 되돌아가 영구 정지했다). 백오프 escalation은 레코드가 아니라 스칼라 `MoveBackoff`가 보존하고, `backoff()`가 지수를 clamp해 int64 wrap으로 인한 음수/0 지연(hot-loop)을 방지한다.

---

## 8. B-4 — Scale-in Drain

### 8.1 삽입 지점 (관측-우선 재구조)

Phase A의 scale-down 거부 분기(`Degraded/ScaleDownRefused` + early-return)를 안전 드레인으로 대체하되, **드레인 경로도 관측 파이프라인을 통과**하도록 `Reconcile`을 재구조화한다.

```go
func Reconcile:
  Get qc
  render children; applyOwned 4 non-STS children (SA/CM/hsvc/csvc)
  Get liveSTS (err==nil):
    if stsImmutableChanged(liveSTS, sts):
        r.refreshObserved(ctx, qc, liveSTS)                  // ★ 읽기전용 B-2 갱신 — status 정지 방지
        Degraded(ImmutableFieldChanged)
        if qc.Status.DrainStatus != nil {                    // ★ 진행 중 드레인은 '일시중단'으로 명시
            qc.Status.DrainStatus.Message = "immutable-drift — STS 수동 재조정 대기, 드레인 일시중단"
            qc.Status.Phase = phaseDraining
        }
        commit; return                                       // STS 미변경(수동 개입 필요)
    if liveSTS.Spec.Replicas != nil && qc.Spec.Replicas < *liveSTS.Spec.Replicas:
        return r.reconcileDrainCycle(ctx, qc, liveSTS)       // 드레인이 STS replicas 를 서수 단위 통제
  applyOwned(sts)                                            // 정상(scale-up 포함)
  return r.reconcileStatus(ctx, qc, sts)
```

`reconcileStatus`(정상 경로)는 **진입 시 무조건 `qc.Status.DrainStatus = nil`**로 정리한다 — 정상 완료(마지막 축소로 물리==목표)와 목표 상향 abort를 이 한 지점이 모두 흡수한다. `reconcileDrainCycle`은 `cur > tgt`가 top-level 가드로 보장되므로 내부 `cur<=tgt` 재확인(도달 불가)을 두지 않는다. 두 경로 모두 `refreshObserved`로 B-2 status를 먼저 갱신하고 §7 통합 lane을 쓴다.

immutable-drift가 진행 중 드레인을 만나도 관측은 갱신되고 일시중단이 명시되므로 "무경고 정지"가 없다. immutable-drift 해소(수동 STS 재조정) 후 다음 reconcile에서 `cur>tgt`가 여전하면 드레인이 재개된다.

### 8.2 peer↔서수 역산 (파싱 실패 안전)

제거 대상은 최고 서수 peer다. `Peer.URI`(`http://<name>-<ordinal>.<name>-headless:6335/`)에서 서수를 역산하되, **어떤 peer의 URI라도 파싱 실패하면 "이미 제거됨"으로 넘기지 않고 즉시 `Degraded(DrainBlocked/PeerURIUnparseable)`로 멈추고 축소를 보류**한다(비표준 부트스트랩 peer가 정상 멤버인데 파싱 실패로 무검증 축소되는 것을 차단).

```go
func peerOrdinal(uri string) (int32, bool) { /* url.Parse → 첫 라벨 → 마지막 '-' 뒤 정수 */ }

// peerOrdinals 는 모든 peer 를 파싱한다. 하나라도 실패하면 ok=false(드레인 차단).
func peerOrdinals(peers []qdrant.Peer) (byOrdinal map[int32]qdrant.Peer, ok bool) {
    byOrdinal = map[int32]qdrant.Peer{}
    for _, p := range peers {
        ord, good := peerOrdinal(p.URI)
        if !good { return nil, false }   // 무검증 축소 금지
        byOrdinal[ord] = p
    }
    return byOrdinal, true
}
```

`reconcileDrainCycle`에서 `ordinals, ok := peerOrdinals(ci.Peers); if !ok { DrainBlocked; return 1m }`. 서수 `cur-1`이 파싱 성공한 ordinals에 **부재**할 때만(= 진짜로 합의에서 이미 제거됨) `shrinkOne(cur-1)`으로 축소만 재개한다.

### 8.3 계획 알고리즘 (순수 함수)

한 pass는 최고 서수 대상 peer(서수 `cur-1`) 하나만 처리한다. keeper = 서수 < 목표 replicas인 peer. 대상의 각 shard에 대해: (1) 그 shard 미보유 최소 부하 keeper로 `Move`(복제 수 보존, load-balancing 결정론), (2) 모든 keeper가 이미 보유(RF>1)면 대상 잉여 복제본 `Drop`, (3) keeper가 받지도 보유도 못 하면 `blocked`(마지막 복제본 유실 위험). 진행 중 transfer 존재 시 `wait=true`. **위상상 blocked는 거의 도달 불가**(목표≥1이라 keeper 최소 1개 존재, RF=1 shard는 항상 이동 가능, RF>1은 드롭 안전) — 오직 운영 실패로만 축소가 막힌다.

### 8.4 실행 시퀀스 (한 pass)

```
refreshObserved → ci := ClusterInfo peers → ordinals,ok(§8.2, !ok→DrainBlocked)
→ settleActiveMove(§7): busy/waiting/lost → publishDrainStatus + return Draining,requeue
→ reshardActive? → Draining + note, 15s (§2.2)
→ target=ordinals[cur-1] 부재 → shrinkOne(cur-1)
→ !allShardsActive → Draining, 10s
→ planPeerDrain(target, tgt, ci.Peers, obs.Collections)
→ publishDrainStatus(선노출: 대상 peers + pendingMoves 를 Status().Update 인라인 커밋)
→ blocked → Degraded(DrainBlocked), STS 불변, 1m
→ wait → Draining, 10s
→ len(moves)>0 → issueActiveMove(moves[0], Kind="Drain")   // §7 공유 lane — 이동/드롭도 유실/백오프 처리
→ else(대상 완전 비었음) → RemovePeer(target, force=false); 실패 시 Degraded+backoff;
                          성공 시 shrinkOne(cur-1)          // 합의 제거 → 그 다음에만 축소
```

`shrinkOne`은 `liveSTS.Spec.Replicas`를 `cur-1`로 재대입해 Update만 한다(목표로 한 번에 밀지 않고 서수 하나씩). 순서 불변식: **이동/드롭 → RemovePeer(force=false) → 축소**. 완료 판정은 다음 pass 관측으로(배치 이동 + 잔여 shard state=Active 확인 → 성급한 제거 방지). `RemovePeer`는 항상 `force=false`(qdrant "shard 잔존 시 거부"를 마지막 안전망으로).

### 8.5 경계·금지 불변식

서수 0 불가침(raft seed) · 목표<1 금지(CRD Minimum=1) · PVC 보존(Retain/Retain, 축소가 PVC 미삭제) · 관측 불가 시 축소 안 함 · 어떤 실패 경로도 STS 축소 금지(구 거부 가드의 안전 계승) · `force=true` 절대 금지 · 마지막 활성 복제본 유실 금지.

### 8.6 spec 재변경 흡수

목표 상향(≥ current) → top-level 가드가 드레인 경로를 안 타고 정상 경로 진입 → `reconcileStatus`가 `DrainStatus=nil`(graceful abort, 이미 제거된 peer는 유지). 추가 감소 → 다음 pass부터 새 keeper 집합으로 재계산(최대 1건 중복 이동의 유계 비효율만 감수).

---

## 9. B-5 — Re-shard 워크플로 (alias 원자 스왑)

`shardNumber`는 생성 후 불변이라, 변경 = 새 shard 수로 shadow 컬렉션 생성 + 데이터 재삽입 + **alias 원자 스왑**과 동치다. `QdrantCollection` 컨트롤러를 확장한다.

### 9.1 트리거 게이트 (오발동 차단)

세 조건 AND로만 실행: (1) `reshardable` — size/distance/RF 일치하고 shardNumber만 다르며 **`spec.shardNumber`가 명시적으로 설정됨**(nil 아님), (2) `spec.reshard == Auto`, (3) `spec.alias != ""`. `spec.shardNumber`가 nil(생략, 흔한 템플릿 습관)이면 라이브 값을 채택할 뿐 절대 리샤드하지 않는다 → 세 겹 명시 opt-in.

```go
func resolvedShardNumber(col *QdrantCollection, live qdrant.CollectionInfo) uint32 {
    if col.Spec.ShardNumber != nil { return *col.Spec.ShardNumber }
    if live.Exists { return live.ShardNumber } // 채택: 라이브 값 — 불일치 없음
    return 1                                   // 신규 생성 기본
}
func reshardable(live qdrant.CollectionInfo, col *QdrantCollection) bool {
    return col.Spec.ShardNumber != nil && live.Exists &&
        live.VectorSize==col.Spec.Vectors.Size && live.Distance==col.Spec.Vectors.Distance &&
        live.ReplicationFactor==col.Spec.ReplicationFactor && live.ShardNumber != *col.Spec.ShardNumber
}
```

`QdrantCollection` reconcile 스위치(세분화):

```go
switch {
case col.Status.Reshard != nil:                 return r.reconcileReshard(ctx, col, cluster, qcl) // 진행 중 구동
case !info.Exists:                              /* 생성(resolvedShardNumber) + alias 보장 */
case paramsMatch(info, desired):                /* 채택/Ready + alias→physical 보장 + activeCollection 확정 */
case reshardable(info, col) && col.Spec.Reshard==ReshardAuto && col.Spec.Alias!="":
                                                return r.beginReshard(ctx, col, cluster, qcl, info)
case reshardable(info, col):                    r.setDegraded(…, "ReshardRequired", "spec.alias 설정 + spec.reshard=Auto 로 opt-in"); return 5m
default:                                         r.setDegraded(…, "ParamsMismatch", …); return 5m
}
```

`paramsMatch`는 `resolvedShardNumber`를 쓴다(nil → 라이브와 일치 → 채택). `Degraded`는 `ReshardRequired`(shardNumber 단독 상이, 해소 가능)와 `ParamsMismatch`(size/distance/RF 상이, 리샤드 불가)로 상호배타 분리된다.

### 9.2 물리/논리 분리

`physical := col.Status.ActiveCollection`(빈 값이면 `col.TargetCollectionName()`). ensure/adopt/삭제/alias는 모두 `physical`을 키로. `spec.alias==""`이면 `physical==TargetCollectionName()`이라 기존 동작과 동일(리샤드 경로 미진입, 회귀 0). `spec.alias!=""`이면 채택/생성 시 `status.activeCollection=physical` 확정 + `UpdateAliases(create_alias: alias→physical)` 멱등 보장.

### 9.3 복사 전략 — scroll + upsert

snapshot 복원은 shard 레이아웃을 스냅샷에 담아 shard 수 변경을 달성 못 하므로, scroll로 읽어 shadow에 upsert 재삽입한다(qdrant가 새 shard 수로 재해시 분산). 복사 루프는 **컨트롤러가 배치 구동**: reconcile당 배치 상한까지 처리 후 `RequeueAfter(짧게)`. 진행률은 `status.reshard.{copiedPoints,totalPoints}`, 재개는 `status.reshard.cursor` 체크포인트(upsert는 point-id 멱등이라 재개/재시도 무중복). 부하 상수화 — 병렬 upsert 없음(§1.1 동시 1건 정합). 발행 직전 `cluster.status.activeMove != nil`이면 유예(§2.2).

### 9.4 상태 기계 + 시퀀스

```
(steady: status.reshard==nil — 관측·ensure·alias 유지만)
  │ reshardable ∧ reshard=Auto ∧ alias!="" [beginReshard]
  ▼
Preparing ─ CreateCollection(shadow,target) ─▶ Copying ─ scroll+upsert 배치 ─(cursor 소진)─▶ Swapping
  │ 실패                                        │ 실패(pre-swap)                                │ ListAliases 확인
  ▼ DeleteCollection(shadow)·원본 무손상        ▼ 델타가드 위반→shadow 폐기                    │ UpdateAliases(원자 delete+create)
Failed ◀── backoff(Attempts++) 재진입 ◀────────┘                                              │ ListAliases 검증(★커밋점)
                                                                                              ▼
                                                     onDelete: Retain→원본 잔존 / Delete→DeleteCollection(원본)  Finalizing
                                                                              │
                                                                              ▼ status.reshard=nil · activeCollection=shadow · Ready
```

1. **beginReshard**: `shadow := fmt("%s-rs-g%d", physical, col.Generation)`; `status.reshard={Preparing, source=physical, shadow, target=*spec.shardNumber, TotalPoints=info.PointsCount, StartedAt}`; `phase=Resharding`.
2. **Preparing**: `GetCollection(shadow)` 없으면 `CreateCollection(shadow,{size,distance,target,rf})`; 존재+일치 재개; 존재+불일치 `Degraded(ShadowConflict)`(임의 삭제 금지). → Copying.
3. **Copying**: `cursor`부터 `ScrollPoints`→`UpsertPoints`→진행률 갱신, 배치 상한 후 requeue. cursor 소진 시 **델타 가드**(`GetCollection(source).PointsCount==TotalPoints`, 위반 시 `Degraded(SourceMutatedDuringReshard)`+shadow 폐기) → Swapping.
4. **Swapping**: `ListAliases`→`UpdateAliases([delete(alias) if 존재, create(alias→shadow)])`→`ListAliases`로 `alias→shadow` 검증(**커밋점**) → Finalizing.
5. **Finalizing**: `status.activeCollection=shadow`; `onDelete==Delete`면 `DeleteCollection(source)` 아니면 `Event(SourceRetained)`; `status.reshard=nil`; `Ready`.

### 9.5 실패·롤백

**커밋점 = alias 스왑.** 스왑 이전(Preparing/Copying) 실패는 전부 가역: shadow는 오퍼레이터 소유 임시 컬렉션이며 alias로 외부 참조된 적이 없으므로 `DeleteCollection(shadow)`로 폐기하고 원본을 무손상으로 남긴다 → `Failed`, `Degraded(ReshardFailed)`, `Attempts++`, backoff 후 동일 shadow 이름 재파생(깨끗한 재시작). 스왑 이후 실패(원본 처분 등)는 롤백하지 않고 원본 처분만 재시도(shadow가 이미 진본). 목표 재변경(`spec.shardNumber != status.reshard.TargetShardNumber`) 감지 시 현재 워크플로 중단(shadow 폐기) 후 새 목표로 재시작. CR 삭제 중(onDelete=Delete) 파이널라이저는 `physical`(=activeCollection) 삭제 + 진행 중 shadow GC.

### 9.6 alias 마이그레이션 (선행 조건)

무중단 스왑은 소비자가 alias로 접근할 때만 성립한다. 단계: (1) 소비자가 물리 이름 직접 사용 → (2) `spec.alias` 설정(컨트롤러가 `create_alias` 멱등 보장, 기존 직접-이름 접근 무영향) → (3) 애플리케이션이 alias로 전환 → (4) 전 소비자 전환 후 `spec.reshard: Auto`. **주의**: 소비자 전환 완료 전에는 `onDelete: Retain`을 유지해야 한다(리샤드 후 원본을 지우면 직접-이름 소비자가 깨진다). 오퍼레이터는 소비자 전환 여부를 알 수 없으므로 이 판단은 선언자 책임이다.

---

## 10. 기존 가드 교체 관계

| 기존(Phase A/B-1/B-3) | 교체·확장 | 안전 보장 계승 |
|---|---|---|
| scale-down 거부(`Degraded/ScaleDownRefused` + early-return) | B-4 `reconcileDrainCycle`로 대체 | "데이터 못 옮기면 물리 크기 불변" — 모든 실패 경로가 STS 축소 안 함 |
| immutable-drift early-return(관측 정지) | 관측 갱신 + 드레인 일시중단 명시 후 return | STS 불변(수동 개입) 유지, status 정지·무경고 제거 |
| `reconcileStatus` 무조건 `Status().Update` | DeepEqual 변경 감지 커밋 + `GenerationChangedPredicate` + `ListCollections` 정렬 | steady-state self-trigger 재기록 제거 |
| `Rebalance *RebalanceMoveStatus`(재배치 전용 추적) | `ActiveMove *MoveStatus`(재배치+드레인 공유) + `MoveBackoff` 스칼라 | 두 스테이지가 동일 lane 정산 — 드레인 후 stale 레코드 허위 실패 제거 |
| lost-command: `IssuedAt=nil`만 지움(영구 정지) | `settleActiveMove` lost 분기가 레코드 nil + clamp 백오프 | 재계획 도달 보장 + hot-loop 방지 |
| `spec.shardNumber uint32` default=1 | `*uint32`(nil=라이브 채택) | 생략(습관)이 리샤드 오발동 안 함 |

---

## 11. 종합 상태 전이도

```
Provisioning ─(전 replica ready)─▶ Running ⇄ Rebalancing        (불균형↔균형, 통합 lane)
     ▲                                │  │
     │ (child 미준비)                  │  └─(spec.replicas<물리)─▶ Draining ─(물리==목표)─▶ Running
     │                                │                            │  │
     └─(scale/generation 변경)────────┘         (목표 상향 abort)──┘  └─(운영 실패)─▶ Draining+Degraded(backoff, STS 불변)

QdrantCollection:  Pending ─▶ Ready ⇄ Resharding(Preparing→Copying→Swapping→Finalizing)
                                 │           │ 실패(pre-swap)
                                 │           └─▶ Failed(backoff)─▶ 재시작
                                 └─(shardNumber 단독 상이·게이트 미충족)─▶ Degraded(ReshardRequired)
```

전역 배타: `Draining`/`Rebalancing`의 이동 발행과 `Resharding`의 백필은 §2.2 비대칭 우선순위로 상호 배타.

---

## 12. 테스트 매트릭스 (envtest fake + 순수 단위)

envtest는 kubelet이 없어 ReadyReplicas가 스스로 오르지 않으므로 기존 `makeReady(name, replicas)`(STS status 직접 patch) 패턴을 재사용한다. 공유 `fakeQdrant` 오염 방지로 각 스펙은 고유 CR 이름 + 서로소 peer id 블록 + 고유 컬렉션명을 쓴다.

| # | 종류 | 시나리오 | 검증 |
|---|---|---|---|
| B2-1 | 단위 | `HTTPClient.ListCollections` 정렬 | golden 응답 순서 무관 정렬 반환 |
| B2-2 | envtest | steady-state 안정성 | 수렴 후 `Consistently`(3s) QdrantCluster status ResourceVersion 불변(self-trigger 재기록 0) |
| B2-3 | 단위 | named-vectors 방어 | 맵 스키마 → 명시 에러(0 오파싱 아님) |
| B3-1 | 단위 | planner(기존 6종 유지) | 단일피어 빈계획·결정론·diff1·종료성·distinct-peer·게이트헬퍼 |
| B3-2 | 단위 | `backoff(level)` | level 1..100 전부 (0, 10m], 음수/0 없음(overflow clamp) |
| B3-3 | 단위 | `settleActiveMove` | 완료→nil+MoveBackoff=0 / 출현창→waiting / 유실→nil+MoveBackoff++·다음 cycle 재계획 도달 |
| B3-4 | envtest | 불균형 수렴·dry-run | (기존, `Status.Rebalance`→`Status.ActiveMove` 갱신) 동시 1건 수렴·dry-run 발행 0 |
| B3-5 | envtest | 드레인 후 재배치 재개 | drain 종료 후 stale ActiveMove가 허위 MoveFailed 안 냄 |
| B4-1 | 단위 | `peerOrdinal`/`peerOrdinals` | 스킴·트레일링 슬래시·이름 내 대시 파싱; 한 개라도 실패→ok=false |
| B4-2 | 단위 | `planPeerDrain` | RF=1 최소부하 keeper Move·균등분산; RF>1 전보유→Drop, 일부 미보유→Move; transfer→wait; 목표1 대상집합 서수0 제외 |
| B4-3 | envtest | 드레인 성공 3→1 | STS replicas→1; RemovedPeers=[최고,차순](서수0 제외); 배치 서수0 peer 수렴; phase Running; DrainStatus nil |
| B4-4 | envtest | 드레인 차단(전송 실패) | `ErrOn[MoveShard]` → `Consistently`(3s) STS 불변; Degraded(MoveFailed); RemovedPeers 빈 |
| B4-5 | envtest | 선노출 | `InFlight` 주차 → phase Draining; DrainStatus.PendingMoves/Peers 실행 전 노출 |
| B4-6 | envtest | 목표 상향 abort | mid-drain 목표↑ → STS 복귀·phase Running·DrainStatus nil |
| B4-7 | envtest | peer URI 파싱 실패 | 비표준 URI peer → Degraded(PeerURIUnparseable), 축소 없음 |
| B4-8 | envtest | 드레인 중 B-2 신선도 | `InFlight` 주차 중 ShardDistribution/Peers가 현재값 반영(정지 아님) |
| B4-9 | 단위 | `Fake.DropReplica` | 배치 제거 + Dropped 로그; `ErrOn[DropReplica]` |
| B5-1 | 단위 | `reshardable`/`resolvedShardNumber` | nil shardNumber 절대 reshardable 아님; 명시 상이→reshardable; size 상이→아님 |
| B5-2 | envtest | 리샤드 happy path | alias 설정 컬렉션 shardNumber 변경+Auto → shadow 생성·복사·alias→shadow 스왑·원본 Retain·activeCollection=shadow·reshard nil·Ready |
| B5-3 | envtest | 게이트 미충족 | shardNumber 상이+alias 없음/Manual → Degraded(ReshardRequired), shadow 생성 0 |
| B5-4 | envtest | T1 회귀 보존 | size 상이 → Degraded(ParamsMismatch), Created/Deleted 0 |
| B5-5 | envtest | 델타 가드 | 복사 중 원본 point 수 변화 → Degraded(SourceMutatedDuringReshard), shadow 폐기 |
| B5-6 | envtest | 상호 배타 | cluster.activeMove 세팅 시 리샤드 유예(shadow 미생성); 컬렉션 reshard 활성 시 재배치/드레인 이동 유예 |

`Fake` 확장: `Points map[string][]json.RawMessage`(scroll/upsert 멱등), `Dropped []string`, `Aliases`/`AliasLog` 재사용, `ErrOn` 키(DropReplica/ScrollPoints/UpsertPoints/ListAliases). RF>1 드롭 end-to-end는 Fake의 shard→단일 peer(RF=1) 모델로 모사 불가 → 순수 `planPeerDrain` 단위 + `Fake.DropReplica` 직접 호출로 결정론 커버.

---

## 13. 단일 적용 롤아웃 (v0.2.0)

### 13.1 순차 게이트

```
make manifests generate fmt vet → make test(envtest 전체) → make lint → parity(불변) → publish-scan → 이미지 빌드/push → 차트 bump → 배포 → 라이브 검증
```

- `parity`는 `internal/resources` 빌더(SA/CM/Service×2/STS)를 건드리지 않으므로 golden 불변(회귀 가드로만 실행).
- CRD 재생성: 신규 struct(`MoveStatus`/`DrainStatus`/`ReshardStatus`)의 deepcopy + `config/crd/bases/*.yaml` + `deploy/chart/templates/crd.yaml`(두 CRD) 재생성이 envtest 노출의 선행.

### 13.2 배포 직후 무행동 논증

단일 peer 라이브 클러스터를 채택한 v0.2.0은 아래로 mutating 연산이 정확히 0임이 구조적으로 보장된다:

- **B-2**: GET 전용. `ListCollections` 정렬 + DeepEqual 커밋 + `GenerationChangedPredicate`로 status 재기록·self-trigger 0.
- **B-3**: `planRebalance`는 `len(peers) < 2`에서 즉시 nil → 단일 peer라 계획 없음 → 이동 0 → `phase=Running, requeue=0`(주기 폴링조차 없음).
- **B-4**: `spec.replicas` 불변 → 드레인 트리거 안 됨.
- **B-5**: 어떤 `QdrantCollection`도 `shardNumber` 명시 상이 + `Auto` + `alias`를 동시 충족하지 않음 → 리샤드 0. 기존 컬렉션은 대부분 `shardNumber` nil(라이브 채택) 또는 일치 → 조용한 Ready.

즉 안정 상태의 유일 활동은 이벤트 구동 관측 GET뿐이다.

### 13.3 라이브 검증 시퀀스

1. 오퍼레이터 파드 `Running` 확인(리더 선출 active).
2. `QdrantCluster` CR가 라이브를 채택: `status.phase=Running`, `status.peers`에 단일 peer, `status.shardDistribution`이 전 컬렉션을 perPeer(0 포함)로 보고, `transfersInFlight=0`.
3. **무변이 소크**: 관측 창 동안 `ShardMoveIssued`/`CollectionCreated`/`CollectionDeleted` Event 0건 + `Consistently` status 안정(§13.2 실측).
4. **격리 B-1 스모크(선택)**: 라이브 데이터와 무관한 신규 이름의 임시 `QdrantCollection` 생성 → Ready → 삭제(기본 Retain이라 데이터 무영향, 또는 Delete로 파이널라이저 경로 검증). 기존 컬렉션·데이터는 절대 건드리지 않는다.
5. **다중 peer 경로(유예)**: 재배치/드레인/리샤드의 라이브 이동, `remote_shards`/`shard_transfers` populated 스키마, RF>1 드롭은 2번째 peer가 필요하므로 스테이징 2-peer 환경에서 실측한다(단일 peer 라이브에서는 실측 불가 — 알려진 한계).

배포 방식은 Helm 차트(`deploy/chart`)의 `image.tag`/`appVersion`을 `0.2.0`으로 상향하는 것으로 수행하며, CRD는 차트 템플릿으로 함께 갱신된다.

