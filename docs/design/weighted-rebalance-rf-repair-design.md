# 크기 가중 리밸런스(2차 기준) + RF>1 자동 재복제 설계 (v0.4.0)

> 승인: 2026-07-21 (A안). Phase B 잔여 갭 2종의 마감 — 성능(크기 배치)과 내구성(replica 수리).

## 결정 (A안 — 통합)

기각: (B) RF 재복제만 — 크기 갭 잔존 / (C) 크기를 1차 기준으로 교체 — 기존 count 종료성
논증 파기 + 진동 위험.

## ① 크기 가중 리밸런스 — count 균형일 때만 진입하는 2차 기준

- **관측 확장**: `CollectionCluster` 의 remote shard 는 points_count 를 제공하지 않는다
  (실측 — 리더 시점 관측의 맹점). `QdrantClientForPeer(qc, ordinal)` 팩토리(기본 =
  `http://<name>-N.<name>-headless.<ns>.svc:6333`)로 각 peer 의 local_shards 를 취합해
  shard→points 완전 지도(`obs.Sizes`)를 만든다. peer 하나라도 실패 → `SizesComplete=false`
  → 크기 단계 전체 스킵(count 기준만 — 안전 강등, 부분 지도로 오판 금지).
- **발동 조건**(모두 하드코드 상수 — YAGNI, spec 노출 없음): count 가 전 쌍 균형(차<2)
  ∧ peer 별 총 points max−min 비율 ≥ **2.0** ∧ 절대차 ≥ **10,000 pts**.
- **이동 선택**: donor(최대 합)→recipient(최소 합), 이동 후 max−min 절대차가 **엄격 감소**
  하는 shard 만 후보(과교정 배제 — recipient 가 새 max 가 되는 이동 금지). 결정론 순서 =
  감소 효과 큰 순 → shardID 오름차순. distinct-peer 제약 동일.
- **종료성**: 매 이동이 max−min 을 엄격 감소시키고 임계 미달 시 정지 → 유한·진동 불가.

## ② RF>1 자동 재복제 — 내구성 수리 (리밸런스보다 우선)

- **목표 RF 소스** = qdrant 컬렉션 설정 `replication_factor`(GetCollection 재사용) —
  QdrantCollection CR 채택 여부와 무관하게 전 컬렉션 커버.
- **발동**: shard 의 Active replica 수 < RF ∧ 합의 peer 수 ≥ RF.
- **계획**: source = 해당 shard 보유 Active peer 중 최소 ID / target = 미보유 peer 중
  (shard 수, peerID) 오름차순 첫 번째. `ReplicateShard`(신규 클라이언트,
  `POST /collections/{c}/cluster {"replicate_shard":{...}}`, method=stream_records) 발행.
- **lane 통합**: 기존 동시-1건 lane 에 `MoveStatus.Kind=Replicate` 추가. 완료 판정 =
  target 에 해당 shard Active 등장(move 와 달리 원본 잔존 — Drop=false 고정).
- **우선순위**: `reconcileRebalance` 진입 시 재복제 계획 먼저, 비어야 리밸런스(count→size).
- **금지**: RF 초과 replica 자동 drop 없음(관측만) — 파괴 동작 불가 원칙.

## 무행동 논증 (라이브 1-peer)

- 재복제: peers(1) ≥ RF(1) 이고 replica 1 ≥ RF 1 → 계획 0. RF 2 컬렉션이 생겨도
  peers < RF 라 발행 불가(관측만).
- 크기: peers < 2 에서 planRebalance 자체가 nil — 불변.

## Fake / 테스트

- Fake 확장: `ReplicateShard`(ExtraReplicas 에 추가), `ExtraReplicas`(RF>1 모사 —
  기존 Placement 단일 매핑 불변으로 회귀 0), `SetShardPoints`(CollectionCluster 가
  PointsCount 합성).
- planner 단위: RF 부족→복제 계획 / 재복제 > 리밸런스 우선 / count 균형+크기 불균형→
  2차 이동 / SizesComplete=false→크기 스킵 / 임계 미달→무행동 / 결정론.
- envtest: RF2 컬렉션 + 2-peer Fake → Replicate 발행·정산 관측.
- 스테이징 실측: 2-peer + RF2 컬렉션 → replica 자동 생성 / 크기 불균형(대형 shard 편중)
  유발 → 크기 이동 관측 → 폐기. 라이브 무행동 재확인.

## 롤아웃

게이트(test/lint/parity/publish-scan) → main → v0.4.0 이미지·태그 → platform/system HR
bump MR → 스테이징 실측 → 라이브 무행동 확인.
