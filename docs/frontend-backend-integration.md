# 프론트-백엔드 연결 현황 정리 (2026-08-12)

프론트(`frontend/`, Vue 3 + Vite)가 지금 어떤 백엔드 요청을 기대하고 있는지, 그중 실제로 존재하는 백엔드 엔드포인트와 맞는지, 안 맞으면 뭐가 다른지, 아예 없는 건 뭔지 정리한 문서다. 프론트는 전용 API 서비스 레이어 없이 각 화면(`views/*.vue`)이 자기 코드 안에서 직접 `fetch`를 호출하는 구조라, 화면 단위로 정리했다.

## 0. 개발 환경 라우팅 (참고)

`frontend/vite.config.js`가 개발 서버 프록시로 이렇게 나눠놓았다:
- `/v1/*` → `http://localhost:8080` (경로 그대로) — `backend`(시세 수집기)
- `/order-api/v1/*` → `http://localhost:8081`, `/order-api` 접두사는 벗겨내고 전달 — `orderapi`(주문 접수 API)

**실제 배포(prod) 환경에는 이 경로 분기가 아직 없다** — `infra/edge.tf`(CloudFront/ALB 설정) 확인 결과 `/order-api` vs `/v1` 같은 경로 기반 라우팅 규칙이 없다. 프론트를 실제로 백엔드에 붙이려면 이 프록시 규칙과 동등한 걸 ALB 리스너 규칙이나 Ingress 경로 규칙으로 인프라 쪽에 만들어야 한다 — 이것도 "연결"의 일부다.

## 1. 이미 맞는 것 — 연결만 하면 됨

| 프론트 화면 | 프론트가 보내는 요청 | 실제 백엔드 | 확인 결과 |
|---|---|---|---|
| `MarketStreamView.vue` | `GET /v1/markets/data?date=` | `backend`의 `GET /v1/markets/data` | 필드 일치 (`markets[].market/batchUrl/streamUrl`) |
| `MarketStreamView.vue` | `GET /v1/markets/{market}/batch?date=` | `backend`의 `GET /v1/markets/{market}/{kind}` | 필드 일치 |
| `MarketStreamView.vue` | `GET /v1/markets/{market}/stream?date=` | 〃 | 필드 일치 |
| `MarketOrderBookView.vue` | `GET /order-api/v1/markets/{market}/orderbook?depth=20` | `orderapi`의 `GET /v1/markets/{market}/orderbook` | 응답 필드(`market`,`timestamp`,`bids[].price/quantity`,`asks[]`)까지 정확히 일치 |
| `OrderManagementView.vue` | `DELETE /order-api/v1/orders/{id}` | `orderapi`의 `DELETE /v1/orders/{orderId}` | 응답 필드(`status`,`canceledAt`,`canceledQuantity`) 일치 |

## 2. 프론트가 잘못 알고 있는 것 — 연결 전에 프론트든 백엔드든 한쪽을 고쳐야 함

### 2.1 `POST /order-api/v1/orders`의 `status: 'DUPLICATE'`
`OrderManagementView.vue`는 응답 `status`가 `ACCEPTED|PARTIALLY_FILLED|FILLED|CANCELED|DUPLICATE` 중 하나로 온다고 가정한다. 그런데 실제 `order.Order.Status`가 가질 수 있는 값은 `ACCEPTED`/`PARTIALLY_FILLED`/`FILLED`/`CANCELED` **뿐**이다(`orderapi/order/order.go`) — `DUPLICATE`는 없다. 같은 `Idempotency-Key`로 재요청하면 캐시된 원래 응답을 **그대로** 재생하므로(예: 원래 `ACCEPTED`였으면 재요청도 `ACCEPTED`), `status` 필드만 보고는 "이건 중복 요청이었다"를 구분할 방법이 없다. 프론트가 이 값을 UI에서 실제로 분기 처리하고 있다면, `DUPLICATE`를 기대하는 코드를 빼야 한다 — 백엔드에 새 상태값을 추가하는 건 `docs/api-specification.md`의 계약을 바꾸는 일이라 별도로 결정할 사안.

### 2.2 "페이퍼 트레이딩 시작" (`AITraderView.vue`) — 필드 자체가 안 맞음
프론트 폼은 `scenarioName`, `selectedDate`, `totalOrders`(기본 1,800,000), `generationTime`(초, 기본 60) → `targetThroughput`(주문/초)을 계산해서 보낼 준비를 하고 있다(실제 요청은 아직 TODO 상태). 그런데 이번 세션에 실제로 만든 시작 엔드포인트(`POST /v1/jobs`, `jobType:"ai-trader"`)가 받는 필드는 `date`, `speed`, `orderBucket`뿐이다 — **"총 주문 수"나 "목표 처리량"이라는 개념 자체가 실제 트레이더(`trader`)엔 없다.** `trader`는 그날 실제 Upbit 시세를 재생하면서 각 봇이 자율적으로 판단해서 주문을 내는 구조라(FR-16), "몇 개를 몇 초 안에 만들어라"처럼 목표치를 지정할 방법이 없다. 프론트 쪽 폼 설계를 실제 파라미터(날짜/배속)에 맞게 다시 짜야 한다 — `totalOrders`/`generationTime`/`targetThroughput`은 백엔드에 대응하는 게 없다.

### 2.3 "재생 시작" (`LoadTestReplayView.vue`) — 일부만 맞음
프론트 폼 필드와 실제 `POST /v1/jobs`(`jobType:"replay"`)의 대응 관계:

| 프론트 필드 | 대응 여부 | 실제 백엔드 |
|---|---|---|
| `pods`(1/2/4/8) | **대응됨** | `shardCount` — 정확히 이 값이 K8s Job의 파드 개수가 된다 |
| `speed`(1×/10×/50×/100×) | **대응됨** | `speed` |
| `recordFile`(예: `burst-market-20-v3.jsonl`) | **대응 안 됨** | 실제로는 `date`(YYYY-MM-DD)로 그 날짜에 `trader`가 기록해둔 주문 파일을 찾아 재생한다 — 임의 파일명을 고르는 개념이 없음 |
| `throughput`(목표 주문/초) | **대응 안 됨** | 재생은 기록된 순서/배속대로 그대로 트는 것이라 목표 처리량을 지정하는 옵션이 없음(2.2와 같은 이유) |
| `market`(5/10/20개) | **대응 안 됨** | 항상 기록된 20개 마켓 전부(또는 샤드가 나눠 맡는 부분집합) 대상 — 마켓 개수를 고르는 옵션이 없음 |

`pods`/`speed`는 그대로 쓸 수 있고, 나머지 세 필드는 프론트 폼에서 빼거나 `date` 선택으로 바꿔야 한다.

**막는 문제 — 이 폼엔 `date` 필드 자체가 없다.** 실제 재생 API는 `date`(YYYY-MM-DD)로 그날 `trader`가 기록해둔 주문 파일을 찾는데, 지금 `recordFile`/`throughput`/`market` 필드 중 날짜에 대응하는 게 없다. **이 폼에 날짜 선택 필드를 추가하지 않으면 진짜 요청 자체를 완성할 수 없다** — 단순 필드 매핑 문제가 아니라 누락된 입력 하나를 새로 추가해야 하는 문제다.

**`pods`(1/2/4/8) 선택지는 백엔드 제약이 아니라 프론트에서 임의로 고른 값이다.** `replayengine`의 샤딩 로직(`i % shardCount == shardIndex`, 대상 마켓 20개)은 1 이상이면 전부 유효하다 — 1~20 사이 어떤 정수든 된다. 20을 넘기면 21번째부터는 담당할 마켓이 안 남아서 그 파드는 그냥 아무 일도 안 하고 끝난다(에러는 아니고 낭비). 그러니 실질적으로 의미 있는 범위는 **1~20**이고, `pods` 드롭다운을 1/2/4/8 네 개로 제한할 이유는 백엔드 쪽엔 없다 — 자유 입력(1~20)으로 바꿔도 무방하다.

### 2.4 `POST /v1/collect` (시세 수집) — **2026-08-12에 응답 방식이 바뀜, 프론트 쪽 수정 필요**
원래는 요청 하나로 20개 마켓 전체를 다 수집하고 끝나면 200으로 결과를 바로 돌려주는 구조였다. 그런데 실제 배포 환경에서 하루치 수집이 193초 걸리는 게 확인됐고, CloudFront의 오리진 응답 대기 한계(180초, 늘릴 수 없는 값)를 넘겨서 504가 났다(팀원이 실제로 재현). 그래서 응답 패턴을 바꿨다:

- `POST /v1/collect`가 이제 **202**를 즉시 돌려준다: `{"jobId": "...", "date": "...", "range": {...}, "status": "IN_PROGRESS"}` — 예전처럼 `results`가 바로 오지 않는다.
- 실제 수집은 백그라운드에서 진행되고, `GET /v1/collect/{jobId}`로 상태를 조회해야 한다 — 완료되면 `status: "COMPLETED"`와 함께 예전에 동기 응답으로 주던 것과 같은 `results` 배열이 온다.

**`AITraderView.vue`가 지금 하는 방식(요청 보내고 응답이 오면 바로 결과 처리)은 더 이상 안 맞는다** — 이 화면은 폴링 로직으로 바꿔야 한다(예: `setInterval`로 몇 초마다 `GET /v1/collect/{jobId}`를 불러서 `status`가 `COMPLETED`가 될 때까지 기다림). 이번 세션에서는 사용자 요청으로 **서버 쪽만 고쳤고 프론트는 그대로 뒀다** — 4.5에 실제 요청/응답 예시를 남겨뒀으니 그걸 보고 프론트 쪽을 고치면 된다.

**추가: 2026-08-19부터 이미 수집된 기간을 다시 요청하면 훨씬 빨리 끝난다.** 원래는 같은 날짜를 다시 `POST /v1/collect`해도 20개 마켓 전부 업비트 API를 처음부터 다시 호출했다(최종 저장 단계에서만 "이미 있으면 건너뛴다"는 체크가 있어서, 정작 제일 오래 걸리는 업비트 호출은 매번 다시 일어남). 이제는 `collectMarket`이 업비트를 부르기 전에 먼저 그 마켓+기간의 batch/stream 파일이 이미 저장돼 있는지부터 확인해서, 이미 있으면 업비트를 아예 안 부르고 기존 경로를 바로 돌려준다.

- **프론트가 알아야 할 것**: `POST /v1/collect` → `GET /v1/collect/{jobId}` 요청/응답 모양 자체는 전혀 안 바뀌었다(4.5 참고) — 폴링 로직을 새로 짤 필요는 없다. 다만 **"이 작업은 몇 분씩 걸릴 수 있다"는 가정을 폴링 UI에 하드코딩하면 안 된다** — 이미 수집된 날짜를 재요청하면 1초 안에(로컬 테스트 기준 384ms) `COMPLETED`가 뜰 수 있다. 반대로 한 번도 수집 안 된 날짜는 여전히 최대 193초 걸릴 수 있으니, 폴링 자체는 그대로 두고 "빨리 끝날 수도, 오래 걸릴 수도 있다"는 전제로 두면 된다.
- 실제 로컬 환경에서 20개 마켓을 전부 미리 저장해둔 뒤 재요청 → 384ms 만에 `COMPLETED`(정상 수집은 실측 193초) 확인함. batch/stream 중 하나만 있는 경우(이전 수집이 중간에 실패한 경우)는 안전하게 다시 수집하도록 처리돼 있음 — 데이터가 깨질 걱정은 없다.

## 3. 아예 없는 것 — 백엔드에 새로 만들어야 함

### 3.1 `DashboardView.vue`의 실시간 지표
주문 접수 TPS, 체결 TPS, 처리 대기 주문, 전체 처리 P99, 실행 중인 Pod 수, "주문·체결 처리량" 라인 차트 — **전부 하드코딩된 문자열이고 fetch 자체가 없다.** 백엔드 어디에도 이 값들을 계산하는 로직이 없다(애플리케이션 레벨 지표 수집기 자체가 없음 — `infra/monitoring.tf`의 Prometheus/Grafana는 CPU/MSK랙/노드 상태 같은 인프라 지표만 본다). 새로 만들어야 할 것:
- TPS(접수/체결) — `orders`/`executions` 토픽 처리량이나 `recorder`의 RDS insert rate를 집계
- 처리 대기 주문 — `matching`의 컨슈머 랙이나 `orderapi`의 in-flight 카운트
- P99 — 주문 접수~체결 사이 지연시간 분포(현재 어디서도 측정 안 함)
- 실행 중인 Pod 수 — K8s API 조회 또는 별도 익스포터
- 이 값들을 계산해서 프론트에 내려줄 API 자체도 없음(계산 로직 + 노출 엔드포인트 둘 다 필요)

### 3.2 `TestResultTrackingView.vue` — 트레이스 조회는 **2026-08-12에 새로 생김**, 나머지는 아직 없음
트레이스 검색(코드 주석엔 `// mock: would call API later`로 표시돼 있던 부분)은 이제 `recorder`의 `GET /v1/trace/{orderId}`로 연결할 수 있다(4.3 참고). 다만 응답 모양이 프론트가 상상한 "API 접수→Kafka 적재→매칭 완료→체결 결과 발행→PostgreSQL" 5단계 타임스탬프와는 다르다 — `recorder`가 실제로 갖고 있는 시각은 주문 접수/체결/취소뿐이라, 그 세 가지만 정확하게 준다. 프론트 화면을 그 실제 모양에 맞게 다시 그려야 한다. NFR 달성치(접수 TPS, E2E p99, Scale-out 시간)는 여전히 없음(3.1과 같은 문제).

### 3.3 `MatchingEngineView.vue` — 엔진 목록은 **2026-08-12에 새로 생김**, 나머지는 아직 없음
"엔진 목록"(어느 엔진이 어느 마켓을 담당 중인지)은 이제 `recorder`의 `GET /v1/matching/engines`로 연결할 수 있다(4.4 참고) — `matching_engine_assignment` 테이블의 `released_at IS NULL`인 행만 모아 엔진별로 묶어서 준다. 매칭 단계/오더북 복구 현황(`replayed/total/missing/timeSec/goalSec`)/체결 내역(Kafka partition·offset 포함) 이 세 개는 여전히 없다 — 이건 매칭 엔진 내부의 순간 상태(크래시 복구 진행률 등)라 RDS에 애초에 저장되는 값이 아니고, 별도로 노출하는 장치가 있어야 한다.

### 3.4 (폐기됨, 2026-08-20) — 구 `RealtimeMonitoringView.vue` / `VITE_GRAFANA_DASHBOARD_URL` 계획
이 절이 설명하던 "Grafana를 iframe으로 띄우는 화면"은 더는 존재하지 않는다 — `RealtimeMonitoringView.vue`는 삭제/대체됐고, 지금은 `DashboardView.vue`가 그 자리를 맡아 자체 렌더링 페이지(하드코딩 placeholder, 3.1 참고)로 재설계돼 있다. `VITE_GRAFANA_DASHBOARD_URL`은 어느 컴포넌트도 읽지 않는 죽은 환경변수라 `frontend/.env.example`에서 제거했다. 여기서 말하던 AMG(Amazon Managed Grafana)도 이 조직 계정엔 IAM Identity Center SSO 권한이 없어 구조적으로 못 쓴다고 이미 결론 나서 폐기됐고, 대신 자체 호스팅 EC2 Grafana(`infra/monitoring-ec2.tf`, `http://monitor.jhyang.click:3000`)를 쓰고 있다. 이 화면을 다시 만든다면 3.1의 애플리케이션 지표를 백엔드가 Prometheus 형식으로 노출한 뒤, 그 EC2 Grafana 대시보드 URL을 iframe으로 붙이는 쪽이 지금 인프라와 맞다.

### 3.5 "AI 트레이더 실행 결과" 화면 — **2026-08-13에 새로 생김**
팀에서 확정한 필드 4가지(실행 상태/주문 접수·체결·미체결 수/시작·종료 시각 및 실행 시간/오류 메시지)를 지원하는 엔드포인트 세 개가 새로 생겼다. `trader`/`replayengine`이 실행을 시작·종료할 때 자동으로 남기는 값이라 프론트가 직접 계산할 게 없다.

- **실행 상태 + 시작/종료 시각 + 오류 메시지** → `orderapi`의 `GET /v1/sessions/last-run` (4.6 참고). `실행 중`은 응답 `status`가 `"IN_PROGRESS"`(`endedAt` 없음)일 때, `완료`/`실패`는 각각 `"COMPLETED"`/`"FAILED"`(`endedAt` 있음)일 때다. 실행 시간은 `endedAt - startedAt`으로 프론트에서 계산하면 된다. 실행이 한 번도 없었으면 404 `NO_RUN_YET`.
- **주문 접수/체결/미체결 수** → `recorder`의 `GET /v1/orders/summary?mode=...&from=...&to=...` (4.7 참고) — `from`/`to`는 위 `last-run` 응답의 `startedAt`/`endedAt`을 그대로 넘기면 된다(실행 중이라 `endedAt`이 없으면 `to`를 생략 — 지금까지 누적치로 응답). 이 구간 지정이 정확한 이유는 `orderapi`의 세션 가드가 트레이더/리플레이 엔진을 동시에 하나만 실행되게 막아서, `[startedAt, endedAt)` 구간에 다른 실행의 주문이 섞일 수 없기 때문이다.
- **실행 파라미터**(날짜/배속 등)는 이 API들에 없다 — 사용자 요청대로, 결과 화면 옆 요청 UI에 이미 그대로 있으니 따로 안 내려줌.
- **"생성 주문 수"/"거절 수"는 의도적으로 뺐다** — `trader`가 봇의 원시 판단(생성) 건수를 API로 노출하지 않고(로그에만 남음), 거절된 주문은 Kafka에 아예 도달하지 않아 시스템 어디에도 흔적이 남지 않는다(구조적으로 관측 불가능). `accepted`/`filled`/`unfilled` 세 값만 있고, `accepted`엔 취소된 주문도 포함되므로 `filled + unfilled`가 `accepted`보다 작을 수 있다(취소분 차이).
- 실제 로컬 Kafka/Redis/MySQL로 `IN_PROGRESS`→`COMPLETED`/`FAILED` 전이, 404(실행 이력 없음), 주문 접수 후 `mode`별 집계, 체결 반영 후 `filled` 카운트 변화까지 전부 직접 확인함.

### 3.6 "부하 시나리오 미리보기" 화면(`LoadTestReplayView.vue`) — **2026-08-19에 새로 생김**
재생 시작 전에 "그날 트레이더가 기록해둔 주문이 총 몇 건이고, 대략 얼마나 걸릴지"를 미리 보여주는 화면. 두 엔드포인트로 지원한다.

- **총 재생 예정 건수 + 예상 소요 시간** → `orderapi`의 `GET /v1/jobs/replay-preview?date=YYYY-MM-DD` (4.8 참고). `totalOrders`는 그날 20개 마켓 전체의 기록된 주문 수 합. `maxEventSpanSeconds`는 배속 1일 때 값이라, 프론트가 재생 시작 폼에서 이미 고른 speed로 **나눠서** 예상 소요 시간을 계산하면 된다(`replayengine`은 마켓마다 독립된 고루틴으로 동시에 재생하므로, 전체 실행 시간은 각 마켓 소요 시간의 **합이 아니라 가장 긴 마켓 하나에 수렴** — 그래서 서버가 마켓별 최댓값을 미리 계산해서 준다). 기록이 없는 마켓은 조용히 건너뛴 값이라 별도 처리 필요 없음.
- **직전 실행과 비교** → `orderapi`의 `GET /v1/sessions/previous-run` (4.9 참고) — `GET /v1/sessions/last-run`(3.5/4.6)과 완전히 같은 응답 모양이지만, "지금 진행 중인/막 끝난 실행"이 아니라 **그 바로 이전 실행**을 준다. 진행 중인 실행을 보여주면서 동시에 "직전엔 어땠는지"를 비교하려면 `last-run`만으론 안 되는데(진행 중인 실행이 시작되는 순간 `last-run` 슬롯을 곧바로 차지해버림), 이 엔드포인트가 그 이전 값을 별도로 보관해뒀다가 준다. 실행이 2번 미만이었으면 404 `NO_PREVIOUS_RUN`. 직전 실행의 접수/체결 수까지 같이 보여주려면, 이 응답의 `startedAt`/`endedAt`을 그대로 `GET /v1/orders/summary`(4.7)에 넘기면 된다 — 단, 직전 실행이 `trader`(페이퍼 트레이딩)였을 수도 있으니 `owner` 필드로 `"replayengine"`인지 먼저 확인하고 보여주는 걸 권장(재생 화면인데 직전이 페이퍼 트레이딩 실행이면 비교 대상으로 부적절).
- **"미체결" 항목은 이 화면에 안 씀** — 팀 결정으로 접수/체결만 보여주기로 함. `GET /v1/orders/summary`의 `unfilled` 필드 자체는 그대로 있으니(AI 트레이더 결과 화면 등 다른 곳에서 씀) 이 화면에서만 그 필드를 안 쓰면 된다 — 백엔드가 따로 응답을 바꾸지 않음.
- 실제 로컬 환경에서 20개 마켓 중 일부만 기록 있는 경우의 합산/최댓값 계산, 마켓 조회 실패가 전체 요청을 막지 않는 것, 두 번째 실행이 시작되는 순간 `previous-run`이 정확히 그 이전 실행으로 갱신되는 것까지 확인함.

## 4. 사용 예시 (실제 요청/응답)

### 4.1 호가창 조회 — `GET /v1/markets/{market}/orderbook`

```
GET /v1/markets/KRW-BTC/orderbook?depth=20
```
```json
{
  "market": "KRW-BTC",
  "timestamp": "2026-08-11T12:00:00.000Z",
  "bids": [{ "price": "153000000", "quantity": "0.05" }],
  "asks": [{ "price": "153010000", "quantity": "0.08" }]
}
```
`depth`는 선택(기본 20, 최대 100). 매칭 엔진이 그 마켓에 아직 스냅샷을 남기기 전이면 `bids`/`asks`가 빈 배열로 온다(에러 아님) — 막 시작했거나 그 마켓에 미체결 주문이 없었던 경우다. 이 스냅샷은 매칭 엔진이 주기적으로 Redis에 비동기로 남기는 것이라, 응답이 그 순간의 실제 상태보다 최대 한 주기 정도 뒤처질 수 있다.

### 4.2 시뮬레이션 시작(샤드 분산 포함) — `POST /v1/jobs`

```bash
curl -X POST http://<orderapi>/v1/jobs \
  -H "Content-Type: application/json" \
  -d '{"jobType":"replay","date":"2026-08-11","speed":500,"shardCount":4}'
```
```json
{ "status": "queued" }
```
(202 Accepted — "요청이 큐에 들어갔다"는 뜻이고, "Job이 실제로 떴다"는 보장은 아니다.)

`shardCount`만 정하면 나머지는 자동이다:
1. `orderapi`가 이 JSON을 그대로 SQS(`team1-sqs-job-trigger`)에 발행.
2. Lambda(`team1-lambda-job-trigger`)가 소비해 `shardCount`만큼 `completions`/`parallelism`을 잡은 K8s Indexed Job을 생성.
3. K8s가 파드마다 자동으로 넣어주는 `JOB_COMPLETION_INDEX`(0, 1, 2, ...)가 그대로 `-shard-index`에 들어가서, 파드마다 저절로 다른 샤드를 맡는다.
4. `-run-id`는 Job 이름 하나를 모든 파드가 공유해서, `orderapi`의 세션 가드가 "같은 실행의 샤드들"로 인식한다.

호출하는 쪽은 개별 샤드 인덱스를 신경 쓸 필요가 없다 — `shardCount` 숫자 하나만 정하면 된다.

**`LoadTestReplayView.vue`의 "재생 시작" 버튼(`onStart()`)이 실제로 이렇게 호출해야 한다** — 지금의 더미 구현(`startMessage.value = '더미 재생 요청이 생성되었습니다'`) 대신:
```js
const onStart = async () => {
  const response = await fetch('/order-api/v1/jobs', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      jobType: 'replay',
      date: selectedDate.value,        // 폼에 없음 — 추가 필요, 아래 참고
      speed: Number(speedOption.value.replace('×', '')),
      shardCount: Number(pods.value),  // "분산 재생기" 선택값 그대로
    }),
  })
  const data = await response.json()
  startMessage.value = response.ok
    ? `재생 작업이 큐에 등록됐습니다 (샤드 ${pods.value}개)`
    : `요청 실패: ${data.message}`
}
```
`/order-api` 접두사는 개발 프록시가 벗겨서 `orderapi:8081`로 전달한다(0장 참고) — prod에서는 그 경로 라우팅이 먼저 만들어져 있어야 한다.

AI 트레이더를 띄울 때는 `jobType`만 바꾸면 된다(샤드 개념 없음, 항상 단일 실행):
```json
{ "jobType": "ai-trader", "date": "2026-08-11", "speed": 60 }
```

**아직 실제 클러스터로 끝까지 검증된 적은 없다** — SQS 발행, Lambda의 K8s Job 생성 로직, K8s Indexed Job 설계는 코드/문서 기준으로 맞게 작성했지만, 이번 세션엔 이 IAM 사용자가 EKS 클러스터 접근 권한이 없어서 실제로 Job이 뜨는 것까지 확인하진 못했다(CLAUDE.md의 "Trader/simulator launch via K8s Job" 참고).

잘못된 요청 시 에러 예시(`INVALID_JOB_TYPE`/`INVALID_DATE`/`INVALID_SPEED`/`INVALID_SHARD_COUNT`):
```json
{ "errorCode": "INVALID_SHARD_COUNT", "message": "shardCount는 1 이상이어야 합니다.", "requestId": "..." }
```

### 4.3 트레이스 조회 — `GET /v1/trace/{orderId}` (2026-08-12 신설, `recorder`)

```
GET /v1/trace/ord_20260812_0000001
```
```json
{
  "orderId": "ord_20260812_0000001",
  "market": "KRW-BTC",
  "side": "BUY",
  "price": "150000000.00000000",
  "quantity": "0.01000000",
  "remainingQuantity": "0.00000000",
  "status": "FILLED",
  "mode": "PAPER_TRADING",
  "submittedAt": "2026-08-12T00:00:00.000Z",
  "executions": [
    {
      "executionId": "exec_...",
      "market": "KRW-BTC",
      "buyOrderId": "ord_20260812_0000001",
      "sellOrderId": "ord_...",
      "price": "150000000.00000000",
      "quantity": "0.01000000",
      "mode": "PAPER_TRADING",
      "executedAt": "2026-08-12T00:00:05.000Z"
    }
  ]
}
```
없는 주문이면 404 `{"errorCode":"ORDER_NOT_FOUND", ...}`. 실제 로컬 MySQL/Kafka/Redis로 이 정확한 요청/응답을 직접 확인함(`recorder`가 아직 K8s에 배포된 적은 없어서, 로컬 환경 기준 검증).

**주의 — 이 응답은 프론트가 기대하는 5단계(API 접수/Kafka 적재/매칭 완료/체결 결과 발행/PostgreSQL) 모양이 아니다.** `recorder`가 실제로 갖고 있는 시각은 `submittedAt`/`executions[].executedAt`/(있다면) `canceledAt` 세 종류뿐이다 — 그 중간 단계들은 어디에도 저장되지 않는다. `TestResultTrackingView.vue`의 트레이스 표시 화면은 이 실제 모양에 맞게 다시 그려야 한다.

### 4.4 매칭 엔진 목록 — `GET /v1/matching/engines` (2026-08-12 신설, `recorder`)

```json
{
  "engines": [
    {
      "engineInstanceId": "engine_28ca71203df2100b",
      "markets": [
        { "market": "KRW-BTC", "assignedAt": "2026-08-12T00:00:00.000Z" },
        { "market": "KRW-ETH", "assignedAt": "2026-08-12T00:00:00.000Z" }
      ]
    }
  ]
}
```
지금 담당 중인(`released_at IS NULL`) 배정만 나온다 — 매칭 엔진이 없으면(아직 아무 마켓도 안 배정됐거나 전부 반납됐으면) `engines`가 빈 배열로 온다. 이 역시 실제 로컬 환경으로 확인함. 매칭 단계/오더북 복구 진행률/체결 내역은 이 엔드포인트에 없다 — 3.3 참고.

### 4.5 시세 수집(비동기) — `POST /v1/collect` + `GET /v1/collect/{jobId}` (2026-08-12 응답 방식 변경)

```bash
curl -X POST http://<backend>/v1/collect -H "Content-Type: application/json" -d '{"date":"2026-07-27"}'
```
```json
{
  "jobId": "job_5872688b79c4b6070671fc5a486aea34",
  "date": "2026-07-27",
  "range": { "start": "2026-07-27T00:00:00+09:00", "end": "2026-07-28T00:00:00+09:00" },
  "status": "IN_PROGRESS"
}
```
(202 Accepted, 즉시 응답 — 실측 0.4초 이내. 이 응답엔 `results`가 없다.)

```bash
curl http://<backend>/v1/collect/job_5872688b79c4b6070671fc5a486aea34
```
진행 중이면:
```json
{ "jobId": "job_...", "date": "2026-07-27", "range": {...}, "status": "IN_PROGRESS" }
```
끝나면(`results`가 예전 동기 응답과 같은 모양으로 채워짐):
```json
{
  "jobId": "job_...", "date": "2026-07-27", "range": {...}, "status": "COMPLETED",
  "results": [
    { "market": "KRW-BTC", "status": "ok", "batchPath": "...", "streamPath": "..." },
    { "market": "KRW-USDT", "status": "error", "error": "..." }
  ]
}
```
모르는 `jobId`면 404. **프론트는 202를 받으면 `jobId`를 저장해두고, `status`가 `COMPLETED`가 될 때까지 몇 초 간격으로 이 상태 조회 엔드포인트를 폴링해야 한다** — 예전처럼 `POST /v1/collect`의 응답 자체에서 결과를 바로 꺼내 쓰면 안 된다(2.4 참고). job 상태는 메모리에만 있어서 `backend`가 재시작되면 사라진다 — 재시작 중에 폴링하던 요청은 404를 받게 되니, 프론트 쪽에서 이 경우도 처리해두면 좋다.

### 4.6 마지막 실행 결과 — `GET /v1/sessions/last-run` (2026-08-13 신설, `orderapi`)

```
GET /order-api/v1/sessions/last-run
```
실행 중일 때(`endedAt` 없음):
```json
{
  "runId": "sess_31b2f15376c9bddf527ff187",
  "owner": "trader",
  "status": "IN_PROGRESS",
  "startedAt": "2026-08-13T00:56:28Z"
}
```
끝났을 때(`status`는 `COMPLETED`/`FAILED` 중 하나, `endedAt`/`message` 채워짐):
```json
{
  "runId": "sess_31b2f15376c9bddf527ff187",
  "owner": "trader",
  "status": "COMPLETED",
  "startedAt": "2026-08-13T00:56:28Z",
  "endedAt": "2026-08-13T00:56:28Z",
  "message": "20개 마켓 전부 성공"
}
```
한 번도 실행된 적 없으면 404 `{"errorCode":"NO_RUN_YET", ...}`. 실행 도중 크래시(정상 종료 경로를 못 타서 `trader`/`replayengine`이 결과를 못 남긴 경우)는 `status`가 계속 `IN_PROGRESS`로 남아있을 수 있다 — 이 값은 세션 자체의 배타적 잠금(TTL 30초)과는 별개로 영구 보관되는 기록이라 자동으로 안 바뀐다. 실제 로컬 Redis로 클레임 직후(`IN_PROGRESS`) → `DELETE /v1/sessions/{id}`로 `COMPLETED`/`FAILED` 반납 → 이 값이 정확히 반영되는 것까지 확인함.

**참고 — `DELETE /v1/sessions/{sessionId}`(주문 취소용 `DELETE /v1/orders/{id}`와 다른 엔드포인트)도 2026-08-13에 요청 본문을 받도록 바뀌었다.** `trader`/`replayengine`이 정상 종료할 때 보내는 요청이라 프론트가 직접 호출할 일은 없지만, 세션 API 응답 모양이 바뀐 배경으로 참고: 본문 `{"status":"COMPLETED"|"FAILED","message":"..."}` (둘 다 선택, 본문 자체가 없으면 `COMPLETED`로 기본 처리 — 기존 프론트/스크립트가 이 엔드포인트를 이미 호출하고 있었더라도 깨지지 않음).

### 4.7 주문 접수/체결/미체결 집계 — `GET /v1/orders/summary` (2026-08-13 신설, `recorder`)

```
GET /v1/orders/summary?mode=PAPER_TRADING&from=2026-08-13T00:56:28Z&to=2026-08-13T01:10:00Z
```
```json
{ "accepted": 1245, "filled": 980, "unfilled": 240 }
```
`mode`는 `PAPER_TRADING`/`REPLAY` 중 하나(필수), `from`은 필수(RFC3339), `to`는 생략하면 지금까지 누적치로 응답(실행이 아직 `IN_PROGRESS`일 때 씀). `mode`/`from`이 없거나 형식이 틀리면 400. `accepted`는 그 구간에 접수된 전체 주문 수(취소분 포함), `filled`/`unfilled`는 그중 체결완료/(접수됨+부분체결) 상태의 개수라 `filled + unfilled`가 `accepted`보다 작을 수 있다(취소된 만큼). 실제 로컬 환경에서 `PAPER_TRADING`/`REPLAY` 각각 주문을 접수해 모드별로 정확히 갈리는 것, `to` 생략 시 지금까지 누적으로 응답하는 것, 체결 반영 후 `filled` 카운트가 실제로 올라가는 것, 데이터 없는 구간엔 `{0,0,0}`으로(에러 아님) 응답하는 것까지 전부 직접 확인함.

### 4.8 주문 재생 미리보기 — `GET /v1/jobs/replay-preview` (2026-08-19 신설, `orderapi`)

```
GET /v1/jobs/replay-preview?date=2026-08-19
```
```json
{
  "date": "2026-08-19",
  "totalOrders": 12483,
  "marketsWithRecords": 18,
  "marketsTotal": 20,
  "maxEventSpanSeconds": 86390
}
```
`date`는 필수(YYYY-MM-DD, `trader`가 그 날짜로 기록했을 때와 같은 값). `marketsWithRecords`는 20개 마켓 중 실제로 그날 기록이 있었던 마켓 수(나머지는 조용히 0건 처리 — 에러 아님). `maxEventSpanSeconds`는 **배속 1일 때** 값이라, 프론트가 재생 시작 폼의 speed 선택값으로 나눠서 예상 소요 시간을 계산해야 한다(예: speed=60이면 `maxEventSpanSeconds / 60`초). 서버가 speed를 안 받는 이유는 프론트에서 speed를 바꿀 때마다 재요청하지 않고 그 자리에서 다시 나누기만 하면 되게 하려는 것. `date` 누락 시 400 `MISSING_DATE`, 형식이 틀리면 400 `INVALID_DATE`. 실제 로컬 환경에서 마켓별 건수 합산, 기록 없는 마켓 자동 제외, 마켓 하나의 조회 실패가 전체 응답을 막지 않는 것(로그만 남기고 계속 진행), 여러 마켓 중 이벤트 시간 범위가 가장 넓은 마켓의 값이 `maxEventSpanSeconds`로 반영되는 것(합이 아니라 최댓값)까지 전부 직접 확인함.

### 4.9 직전 실행 조회 — `GET /v1/sessions/previous-run` (2026-08-19 신설, `orderapi`)

```
GET /v1/sessions/previous-run
```
응답 모양은 4.6의 `last-run`과 완전히 동일:
```json
{
  "runId": "run_prev123",
  "owner": "replayengine",
  "status": "COMPLETED",
  "startedAt": "2026-08-18T09:02:11Z",
  "endedAt": "2026-08-18T09:14:37Z",
  "message": "20개 마켓 재생, 0개 건너뜀 (shard 1/1)"
}
```
"지금 진행 중인/방금 끝난 실행"이 아니라 **그 바로 이전 실행**을 준다 — 새 실행이 시작되는 순간 `last-run`이 그 실행 것으로 즉시 바뀌기 때문에, "진행 중 화면"과 "직전 실행과 비교"를 동시에 보여주려면 이 별도 엔드포인트가 필요하다. 실행이 2번 미만이었으면(한 번도 없었거나 첫 실행 도중이면) 404 `NO_PREVIOUS_RUN`. `owner`가 `"trader"`일 수도 있으니(직전이 페이퍼 트레이딩이었던 경우), 재생 화면에서 비교 대상으로 쓰기 전에 `owner === "replayengine"`인지 프론트에서 먼저 확인하는 걸 권장 — 아니면 그냥 "직전 재생 실행 없음"으로 처리. 실제 로컬 Redis로 첫 실행 완료 → 404 아님 확인 → 두 번째 실행 시작 → 그 순간 `last-run`은 새 실행, `previous-run`은 정확히 첫 실행으로 갈리는 것까지 확인함.

## 요약

- **바로 연결 가능**: 시세 조회, 호가창 조회, 주문 접수/취소 (5개 화면 대응 경로, 2개 필드 불일치만 정리하면 됨)
- **필드만 고치면 됨**: 페이퍼 트레이딩 시작, 재생 시작 (실제 시작 엔드포인트는 이미 있음 — `POST /v1/jobs`)
- **응답 방식이 바뀜, 프론트 수정 필요**: 시세 수집(`POST /v1/collect`) — 동기 200에서 비동기 202+폴링으로 바뀜(2.4/4.5 참고), 서버 쪽은 이미 반영됨
- **2026-08-12에 새로 생김**: 트레이스 조회(`GET /v1/trace/{orderId}`), 매칭 엔진 목록(`GET /v1/matching/engines`) — 둘 다 `recorder`, 응답 모양은 프론트 목업과 다르니 화면 쪽 재설계 필요(4.3/4.4 참고)
- **2026-08-13에 새로 생김**: AI 트레이더 실행 결과 화면용 세 엔드포인트 — `orderapi`의 `GET /v1/sessions/last-run`(실행 상태/시작·종료 시각/오류 메시지, 4.6 참고)과 `recorder`의 `GET /v1/orders/summary`(주문 접수/체결/미체결 수, 4.7 참고). 둘 다 프론트가 직접 계산할 게 없는, 그대로 쓸 수 있는 응답(3.5 참고)
- **2026-08-19에 새로 생김**: 부하 시나리오 미리보기 화면용 두 엔드포인트 — `orderapi`의 `GET /v1/jobs/replay-preview`(재생 예정 건수 + 배속 1 기준 소요 시간, 프론트가 speed로 나눠 씀, 4.8 참고)와 `GET /v1/sessions/previous-run`(직전 실행 기록, `last-run`과 같은 모양, 4.9 참고). 이 화면은 접수/체결만 보여주고 미체결은 의도적으로 뺌(3.6 참고)
- **여전히 새로 만들어야 함**: 대시보드 실시간 지표 전체(TPS/P99/대기주문/Pod수), NFR 달성치, 매칭 단계/오더북 복구 진행률/체결 내역 — 계산 로직 + 조회 API 둘 다 없음
- **인프라 작업 별도 필요**: prod 환경의 `/v1` vs `/order-api`(+`recorder`용 새 경로) 라우팅, Grafana 대시보드 구성
