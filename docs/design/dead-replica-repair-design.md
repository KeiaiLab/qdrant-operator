# Dead replica 수리 설계 (v0.9.0)

> 승인: 2026-09-10. Phase B 리밸런스 루프가 스스로 잠기는 경로의 제거.

## 문제 — 안전 게이트가 데드락이 된다

`reconcileRebalance` 는 비-Active shard 가 하나라도 있으면 계획 단계 전체를 보류했다.
전송 중(`Initializing`/`Partial`/`Recovery`)에 새 이동을 얹지 않으려는 보수적 게이트다.

그런데 `Dead` 는 전이 상태가 아니라 **종착 상태**다. peer 가 영구 이탈하면 그 사본은
Dead 로 굳고 스스로 벗어나지 못한다. 결과:

1. 잃어버린 내구성을 되찾으라고 만든 **RF 재복제가 발동하지 못한다** — 고쳐야 할 조건이
   고치는 경로를 막는다
2. Dead 항목은 사라지지 않으므로 count·크기 리밸런스도 **영구 차단**된다

`nodefailure.go` 는 죽은 노드의 파드만 회수한다. PVC 데이터가 함께 사라졌다면 Dead 는
그대로 남는다 — peer 하나의 영구 이탈이면 충분히 재현된다.

## 게이트 분리

```
        shard state
             │
   ┌─────────┼──────────────────────────┐
   │         │                          │
 Active    Dead                 Initializing/Partial/…
   │         │                          │
   │    종착 — 통과                 전이 — 보류
   │    (수리 대상)                (기존 동작 불변)
   └─────────┴──────────────────────────┘
```

- `transitioning()` — Active 도 Dead 도 아닌 shard 존재. 참이면 전 계획 보류.
- `hasDead()` — Dead 잔존 여부. 성능 단계 진입 차단 판정.
- `allShardsActive()` — **변경 없음**. B-4 드레인은 계속 엄격 게이트를 쓴다(축소는
  되돌릴 수 없어 완전히 조용한 클러스터를 요구한다).

## 계획 우선순위

```
planReplications   내구성 회복   ← Dead 를 holder 로 세지 않는다
      ↓ 비면
planDeadDrops      잔해 회수     ← Active >= RF 인 shard 의 Dead 만
      ↓ 비면
planRebalance      성능(count→크기)  ← Dead 잔존 시 진입 금지
```

순서가 곧 안전 불변식이다.

- 재복제가 **먼저**라 회수는 항상 RF 를 충족한 뒤에만 일어난다. Active 가 RF 미달이면
  `planDeadDrops` 는 그 shard 를 건너뛴다 — 마지막 성한 사본을 지울 경로가 없다.
- 리밸런스가 **마지막**이라 잔해가 섞인 count 로 균형을 오판하지 않는다.
- `planReplications` 의 target 후보에서 그 shard 의 Dead 보유 peer 를 제외한다. 죽은
  사본이 있는 peer 로 복제를 쏘면 실패한다(기존 게이트 아래서는 도달 불가였던 경로다).

## 파괴 동작 예외

`weighted-rebalance-rf-repair-design.md` §② 는 자동 drop 을 금지했다. 그 금지는
**RF 를 충족하는 Active replica** 에 대해 유효하고, Dead 는 예외로 둔다.

- Dead replica 는 읽기도 쓰기도 받지 못하는 잔해다 — 지워도 잃을 데이터가 없다
- Active >= RF 를 확인한 뒤에만 지운다 — 내구성이 줄어드는 순간이 없다
- 남겨두면 리밸런스 전체가 사람의 수동 개입을 기다리며 멈춘다. 사람이 손대야 비로소
  다시 도는 관문을 만들지 않는 것이 이 오퍼레이터의 기본 방침이다

새 `Degraded` 사유는 만들지 않았다. 조건은 켠 쪽이 끈다는 기존 규율(`clearMoveFailed`)을
깨면 다른 경로의 신호를 삼킨다. Dead 의 가시성은 `status.plannedMoves`(`coll/shard: drop@peer`)
와 이벤트(`ReplicaDropIssued`)로 확보한다.

## 실행 lane

신규 배관 없음. 기존 동시-1건 lane 을 그대로 쓴다 — `issueActiveMove` 의 `Drop` 분기가
`DropReplica` 를 호출하고, 완료 판정(source 가 더는 그 shard 를 보유하지 않음)이 그대로
성립한다.

## 무행동 논증

Dead replica 가 없으면 `transitioning()` 은 기존 `allShardsActive()` 의 부정과 정확히
같은 값을 내고 `planDeadDrops` 는 빈 계획을 낸다. **행동 델타 0** — 건강한 클러스터에서
이번 변경은 관측되지 않는다.

## 테스트

- 단위: Dead 는 전이가 아님 / 진짜 전이는 여전히 보류 / target 에서 Dead 보유 peer 제외 /
  Active>=RF 일 때만 회수 / Dead 잔존 시 리밸런스 보류
- envtest: 3-peer + RF2 컬렉션에 Dead 주입 → 성한 peer 로 재복제 → 회수 → 전 Active 복귀 →
  무행동 정착. **수정을 되돌리면 첫 단계에서 타임아웃**(재복제 미발행)으로 교착이 재현된다.
- Fake 확장: `MarkDead` / `DropReplica` 가 실제로 사본을 걷어냄(관측 기반 완료 판정 성립).
