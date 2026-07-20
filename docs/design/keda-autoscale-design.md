# KEDA 오토스케일 연동 설계 (v0.3.0)

> 승인: 2026-07-21 (B안). Phase B(v0.2.1)에서 실증된 무중단 재배치 위에 부하 기반
> replicas 결정 계층을 KEDA 로 얹는다.

## 결정: CRD scale subresource + KEDA → QdrantCluster (B안)

KEDA ScaledObject 는 `/scale` subresource 를 정의한 custom resource 를 직접 스케일할 수
있다 (KEDA scaledobject-spec 공식 문서, 2.17). 따라서:

- **KEDA = 결정**: 메트릭 임계 판정 → `QdrantCluster` 의 `/scale` 로 `spec.replicas` 조정
- **오퍼레이터 = 실행**: replicas 증가 → peer join + rebalance / 감소 → drain
  (이동→RemovePeer→축소) — Phase B 스테이징 실측 완료 경로 그대로

기각 대안: (A) KEDA→STS 직접 — 오퍼레이터가 STS 를 소유(`applyOwned` 가
`qc.spec.replicas` 로 항상 재적용)하므로 소유권 충돌. (C) 오퍼레이터 내장
오토스케일러 — KEDA 재발명, 복잡성 최소화 원칙 위반.

## 오퍼레이터 변경 (v0.3.0)

1. `QdrantCluster` CRD 에 scale subresource:
   `specpath=.spec.replicas / statuspath=.status.readyReplicas / selectorpath=.status.selector`
2. `status.selector` 신설 — `SelectorLabels` 의 label-selector 직렬화 문자열.
   prometheus(External) 트리거는 selector 를 요구하지 않지만, 향후 cpu/memory
   (Resource) 트리거 확장 시 HPA 의 파드 발견에 필수라 지금 채운다(비용 1줄).
3. envtest: `/scale` GET(replicas/selector 노출) + PUT(replicas 변경이 spec 에 반영)
   → 이후 기존 scale-up/drain 경로는 기존 spec 이 회귀 검증.

## fleet ScaledObject (platform/system)

buildkitd(STS + prometheus + behavior) 라이브 패턴 재사용. 실측 근거:
vmsingle 에 `container_memory_working_set_bytes`(838Mi)·`container_spec_memory_limit_bytes`(4Gi)
시계열 실재 — 현 사용률 ~20%.

```yaml
scaleTargetRef:
  apiVersion: qdrant.keiailab.com/v1alpha1
  kind: QdrantCluster
  name: platform-data-qdrant
minReplicaCount: 1        # 데이터 서비스 — scale-to-zero 금지
maxReplicaCount: 3        # PVC 10Gi×3 + 노드 여유 기준 (명시 가정)
pollingInterval: 30
triggers:
- type: prometheus        # 파드 평균 메모리 사용률 (qdrant = 메모리 바운드, CPU 유휴 실측)
  metadata:
    serverAddress: http://vmsingle-server.observability.svc.cluster.local:8428
    query: sum(container_memory_working_set_bytes{namespace="data",pod=~"platform-data-qdrant-.*",container="qdrant"})
           / sum(container_spec_memory_limit_bytes{namespace="data",pod=~"platform-data-qdrant-.*",container="qdrant"})
    threshold: "0.7"
    ignoreNullValues: "true"
advanced:
  horizontalPodAutoscalerConfig:
    behavior:
      scaleUp:   { policies: [{type: Pods, value: 1, periodSeconds: 60}] }
      scaleDown: { stabilizationWindowSeconds: 1800, policies: [{type: Pods, value: 1, periodSeconds: 600}] }
```

scaleDown 안정화 1800s = buildkitd(600s)의 3배 — shard 이동은 데이터 복사라 진동 절대
금지. qdrant 앱 메트릭(`rest_responses_total` 등)은 vmagent 미수집(실측 series 0)이라
스크레이프 배선은 범위 밖(YAGNI) — 컨테이너 메트릭으로 충분.

## 안전 논증

- 현 사용률 20% < 0.7 → 부착 즉시 desired=1, **무행동 착지**.
- scale-up: HPA 1 pod/60s → 오퍼레이터 join+rebalance (실증 경로).
- scale-down: 1800s 안정화 후 1 pod/600s → 오퍼레이터 drain (대피 후 축소, 실증 경로).
- drain 진행 중 재상승: `spec.replicas` 재증가 → drain 진입 조건
  (`spec.replicas < liveSTS.replicas`) 자연 해제 — 기존 로직.
- KEDA 장애 시: ScaledObject 미동작 = replicas 불변 = 현상 유지 (fail-safe).

## 롤아웃·검증 태스크

1. CRD 마커 + `status.selector` + envtest → 게이트(test/lint/parity/publish-scan) → main
2. v0.3.0 이미지(빌드→unarchive→push→re-archive) + GitHub 태그
3. platform/system 1 MR: HR values `tag: v0.3.0` + ScaledObject 추가
   (CRD 는 GitRepository(main) 경유 자동 반영 — 이미지보다 선행 무해:
   prometheus 트리거는 selector 불요, readyReplicas 는 기존 필드)
4. 스테이징 실측: 임시 QdrantCluster + 임시 ScaledObject(threshold 를 현 사용률
   아래로 조작) → KEDA 가 /scale 로 2 승격 → rebalance 관측 → threshold 복원 →
   안정화 창 후 drain 복귀 관측 → 폐기
5. 라이브: ScaledObject Ready + HPA desired=1 유지(무행동) + qdrant 무접촉 확인
