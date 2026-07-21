# 노드 장애 파드 자동 정리 설계 (v0.6.0)

> 실측 갭: 노드가 영구 이탈하면 StatefulSet 파드가 Terminating/Unknown 으로 갇히고
> 대체 파드가 생성되지 않는다(ordinal 단일성 보장). 서비스는 RF2 로 지속되지만
> replicas 2 → 실제 1 로 HA 가 조용히 소실된다.

## 문제 (K8s 표준 동작)

1. 노드 NotReady → 파드 toleration(300s) 만료 → 파드에 `deletionTimestamp` 부착
2. kubelet 부재 → 삭제 확정 불가 → 파드가 Terminating 으로 잔존
3. StatefulSet controller 는 같은 ordinal 파드가 살아 있다고 보고 **대체 파드를 만들지 않음**
4. 결과: 사람이 `--force --grace-period=0` 로 치우거나 `out-of-service` taint 를 붙여야 복구

우리 클러스터 실측: K8s v1.36(out-of-service GA 존재) / node-healer 는 SSH 재부팅만 수행
(taint 로직 0) / ceph-rbd 는 네트워크 스토리지라 **다른 노드 attach 는 이미 가능**
(eviction 실증에서 pod-1 이 e22→e21 이동). 즉 막힌 것은 스토리지가 아니라 "죽은 파드 정리".

## 결정: 오퍼레이터가 자기 파드만 force delete

노드 taint(`out-of-service`)는 그 노드의 **모든** 워크로드를 축출하는 클러스터 전역 조치라
오퍼레이터 스코프를 넘는다. 대신 자기 STS 파드만 강제 삭제하면 StatefulSet 이 즉시 대체
파드를 만들고, 볼륨 detach 는 CSI(RBD watcher 만료)가 처리한다 — 영향 범위가 자기
워크로드로 한정되어 안전하다.

기각: (a) out-of-service taint 자동 부착 — 타 워크로드 연쇄 축출 위험, 오퍼레이터 권한 과다.
(b) 무행동(현행) — HA 조용한 소실 방치.

## 발동 조건 (전부 충족해야 삭제)

1. `spec.replicas >= 2` — 단일 파드 클러스터는 대체 파드도 같은 상황이라 무의미
2. 파드가 배치된 노드가 **NotReady**(`Ready != True`) 또는 **노드 객체 부재**
3. 그 상태가 `nodeFailureGrace`(기본 **6분** = toleration 300s + 여유 60s) 이상 지속
   - 노드 NotReady 는 `Ready` condition 의 `LastTransitionTime`, 노드 부재는 파드 기준
4. 파드가 이미 `deletionTimestamp` 를 갖고도 남아 있거나(갇힘), Running/Unknown 상태
5. **동시 1개만** — 여러 노드 동시 장애 시 연쇄 축출로 정족수를 깨지 않는다

## 안전 불변식

- PVC/PV 는 건드리지 않는다(데이터는 네트워크 스토리지에 그대로).
- 삭제는 파드 1개 `GracePeriodSeconds=0`. STS·PVC·CR 은 무관.
- 노드가 Ready 로 복귀하면 아무 것도 하지 않는다(조건 2 불충족).
- 이벤트 `StuckPodDeleted` 로 감사 흔적을 남긴다.

## 구현

- `internal/controller/nodefailure.go`: `reconcileStuckPods(ctx, qc) (int, error)`
  - STS selector 로 파드 목록 → 노드 조회 → 조건 판정 → 첫 후보 1개 force delete
- Reconcile 진입부(관측 전)에서 호출 — 이동/드레인 lane 과 무관(파드 정리는 인프라 층).
- RBAC: `nodes` get/list/watch + `pods` get/list/delete 추가.

## 검증

- envtest: 노드 객체 2개(Ready/NotReady) 생성 → NotReady 노드에 파드 배치 →
  ① grace 미달이면 미삭제 ② grace 초과면 삭제 ③ replicas=1 이면 미삭제
  ④ 노드 Ready 면 미삭제
- 라이브: 정상 상태에서 무행동(노드 전부 Ready) 확인. 실제 노드 영구 이탈 재현은 불가하므로
  조건 판정을 envtest 로, 삭제 경로는 기존 force delete 실증(failover 474회)으로 근거.
