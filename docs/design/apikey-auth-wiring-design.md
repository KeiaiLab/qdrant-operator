# apiKey 인증 배선 설계 (이슈 #4)

## 배경

`QdrantClusterSpec.apiKey`(`*SecretKeyRef`)는 CRD·Go 타입에 **선언만** 돼 있고 컨트롤러가 이 필드를 소비하지 않는다. CR에 `apiKey`를 지정해도 StatefulSet에 인증 env가 주입되지 않아 Qdrant는 무인증으로 기동한다(silent no-op). `retentionPolicy` unwired(v0.2.1 fix)와 동일 계열.

현재 완화책은 NetworkPolicy/Cilium 실효 통제지만, 이는 L3/L4(누가 연결하나)라 application-level authz(무엇을 하나)를 대체하지 못한다. 허용된 소비자 ns는 무인증 전체 접근(파괴 API 포함)이다.

## 목표

1. `spec.apiKey`(쓰기 키)를 `QDRANT__SERVICE__API_KEY`로 배선한다.
2. 신규 `spec.readOnlyApiKey`(읽기 전용 키)를 `QDRANT__SERVICE__READ_ONLY_API_KEY`로 배선한다 — 검색만 하는 소비자에게 최소권한 키 발급을 가능케 한다.
3. TLS 비활성 상태에서 키가 설정되면 경고를 남긴다(게이팅하지 않음, 인증은 활성).

## 비목표 (YAGNI)

- 소비자별 키 분배·회전 자동화(운영 영역, 오퍼레이터 밖).
- TLS 강제 게이팅(별개 축의 결정 — 클러스터 내부 트래픽에 api_key 단독은 통상 패턴).
- production.yaml에 api_key 기입(ConfigMap = 평문 노출이므로 금지).

## 설계

### 1. CRD 타입 (`api/v1alpha1/qdrantcluster_types.go`)

```go
APIKey         *SecretKeyRef `json:"apiKey,omitempty"`          // 기존 (쓰기)
ReadOnlyAPIKey *SecretKeyRef `json:"readOnlyApiKey,omitempty"`  // 신규 (읽기 전용)
```

`SecretKeyRef`는 재사용(`Key` 기본값 `"api-key"`). optional 필드 추가라 기존 CR은 nil로 읽혀 하위호환 안전. `make generate`(deepcopy) + `make manifests`(CRD yaml, config/crd + deploy/chart 동기)로 재생성.

### 2. env 배선 (`internal/resources/statefulset.go`)

`readOnlyRootFilesystem=true`라 config 파일 런타임 수정 불가 → qdrant의 `QDRANT__<SECTION>__<KEY>` 이중언더스코어 env override 사용. Secret 값은 `valueFrom.secretKeyRef`로 주입해 평문 노출 0.

```go
// apiKeyEnv 는 설정된 키에 한해 QDRANT 인증 env 를 secretKeyRef 로 만든다.
// 미설정 CR 이면 빈 슬라이스 → golden(helm template) parity 유지.
func apiKeyEnv(qc *qdrantv1alpha1.QdrantCluster) []corev1.EnvVar
```

기존 `Env`(`QDRANT_INIT_FILE_PATH`)에 `append(base, apiKeyEnv(qc)...)`. **미설정 시 추가 0**이 핵심 — `BuildStatefulSet` golden parity 주석("필드 임의 추가 금지")을 지킨다. `SecretKeyRef.Key`가 빈 문자열이면 `"api-key"` fallback(빌더 방어; CRD default가 이미 채우지만 직접 생성한 CR 대비).

env 순서: 결정론 위해 base → apiKey → readOnlyApiKey 고정.

### 3. TLS-off 경고 (`internal/controller/qdrantcluster_controller.go`)

reconcile 흐름에서 `(APIKey != nil || ReadOnlyAPIKey != nil) && !Config.TLSEnabled`이면:

```go
commonsevents.EmitWarningf(r.Recorder, qc, "AuthWithoutTLS",
    "apiKey 가 설정됐으나 TLS 비활성 — 키가 평문으로 전송됩니다")
```

기존 `ImmutableFieldChanged` 가드와 동일 패턴(commons events 어댑터). 게이팅·에러 반환 없음 — 인증은 정상 활성. 로그도 함께.

## 테스트 (`internal/resources/statefulset_test.go`)

표준 `testing` + env 순회 검증(`TestBuildStatefulSet_Entrypoint` 패턴 따름):

1. `미설정` → `QDRANT__SERVICE__*` env 부재(golden parity). env 개수 = 기존과 동일.
2. `apiKey만` → `QDRANT__SERVICE__API_KEY` 존재, `ValueFrom.SecretKeyRef.{Name,Key}` 정확.
3. `readOnlyApiKey만` → `QDRANT__SERVICE__READ_ONLY_API_KEY` 존재.
4. `둘 다` → 둘 다 존재.
5. `Key 미지정` → `"api-key"` fallback.

컨트롤러 TLS-off 경고는 기존 envtest 스위트(`suite_test.go`) 관례를 따르되, 최소한 빌더/조건 단위 검증으로 커버.

## 수용 기준

- CR에 `apiKey` 지정 → STS env에 `QDRANT__SERVICE__API_KEY`(secretKeyRef) 주입 → 무인증 REST 요청이 401.
- 미설정 CR → 기존 STS와 diff 0(golden parity).
- **envtest는 이 갭을 못 잡았으니**(선언≠배선), 격리 스테이징에서 무인증 401 실측을 최종 게이트로.

## 롤아웃

- optional 필드 추가 + 조건부 env라 라이브 CR(`apiKey` 미지정)에는 무행동 → dark landing 아님, 회귀 0.
- 실제 인증 활성화는 별도 작업: Secret 생성 → CR에 `apiKey`/`readOnlyApiKey` 지정 → 소비자 egress에 키 배포. 이 PR은 **메커니즘만** 제공.
