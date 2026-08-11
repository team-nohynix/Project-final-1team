# API 명세서

## 변경 이력
| 날짜 | 변경 내용 |
|---|---|
| 2026-07-31 | 요구사항 정의서(requirements.md) 기반으로 접수 API 명세서 최초 작성 (FR-01~04, FR-10, FR-12~13, FR-22 대응) |

## 관련 문서
- 본 문서는 [요구사항 정의서](requirements.md)의 FR-01~04, FR-10, FR-12~13, FR-22를 근거로 작성되었다.
- Kafka 메시지 규격, 주문 기록 파일 규격은 별도 문서에서 관리한다(FR-22).
- 역할 분담 기준(requirements.md 1.2.4)에 따라 본 API는 A 담당(주문 접수·조회)이 구현·운영한다.

---

## 1. 개요

### 1.1 기본 정보
| 항목 | 내용 |
|---|---|
| Base URL | `https://api.truss.internal/v1` |
| 프로토콜 | HTTPS, JSON (Content-Type: `application/json; charset=utf-8`) |
| 버전 관리 | URL 경로에 메이저 버전 포함(`/v1`). 하위 호환 깨지는 변경 시 버전 상향 |
| 인증 | 내부망 전용 시스템으로 사용자 인증 없음. 서비스 간 통신만 허용(NFR-18) |
| 대상 마켓 | 업비트 원화 마켓 20개(요구사항 정의서 1.1.4). 목록 외 마켓명은 전부 오류 처리 |

### 1.2 공통 규칙
- 가격(`price`), 수량(`quantity`)은 부동소수점 오차 방지를 위해 **문자열(String)** 로 표현한다. 예: `"71500000"`
- 시각은 ISO-8601 UTC(`2026-07-31T09:00:00.000Z`)로 표현한다.
- 정상 처리는 2xx, 클라이언트 오류는 4xx, 서버 오류는 5xx로 응답한다.
- 모든 오류 응답은 아래 공통 포맷을 따른다(4장 참고).

### 1.3 공통 요청 헤더
| 헤더 | 필수 | 설명 |
|---|---|---|
| `Content-Type` | Y | `application/json` |
| `Idempotency-Key` | 주문 접수 시 필수 | 클라이언트가 발급하는 고유 키. 동일 키로 재요청 시 최초 응답을 그대로 반환(FR-02) |
| `X-Request-Id` | 선택 | 요청 추적용 UUID. 미전달 시 서버가 발급하며 응답 헤더로 반환(FR-21 구간별 소요 시간 추적과 연계) |
| `X-Order-Mode` | 선택 | `PAPER_TRADING`(기본값) 또는 `REPLAY`. 이 주문이 페이퍼 트레이딩(AI 트레이더)인지 리플레이(시뮬레이터)인지 구분 — "기록기"가 `TRADE_ORDER.mode`(erd.md)를 채우는 데 씀. 미전달·알 수 없는 값은 처리 정합성에 영향 없이 `PAPER_TRADING`으로 기본 처리(경고 로그만 남김) — `Idempotency-Key`와 달리 라벨링용이라 필수로 강제하지 않음 |

---

## 2. 주문 접수 API

### 2.1 주문 접수
`POST /v1/orders`

마켓명·수량·가격·가격 단위의 유효성을 검증한 뒤 접수하고 주문 번호를 반환한다(FR-01). 접수된 주문은 마켓명 기준 파티션으로 Kafka `orders` 토픽에 발행된다(FR-04).

**Request Body**
| 필드 | 타입 | 필수 | 설명 |
|---|---|---|---|
| `market` | string | Y | 마켓 코드. 예: `KRW-BTC` (20개 목록 중 하나) |
| `side` | string | Y | `BUY` \| `SELL` |
| `price` | string | Y | 주문 가격(KRW). 마켓별 호가 단위(tick size)의 배수여야 함 |
| `quantity` | string | Y | 주문 수량. 0보다 커야 함 |
| `sourceOrderId` | string | N | 리플레이 주문만 사용 — 재생 중인 원본 페이퍼 트레이딩 주문의 `orderId`. 검증에는 관여하지 않고 그대로 Kafka `orders` 토픽에 실려 "기록기"가 `TRADE_ORDER.source_order_id`(docs/erd.md)를 채우는 데만 쓰인다. 신규(페이퍼 트레이딩) 주문은 이 필드를 보내지 않는다 |

```json
{
  "market": "KRW-BTC",
  "side": "BUY",
  "price": "71500000",
  "quantity": "0.015"
}
```

**Response 202 Accepted** — 정상 접수

```json
{
  "orderId": "ord_20260731_0000001",
  "market": "KRW-BTC",
  "side": "BUY",
  "price": "71500000",
  "quantity": "0.015",
  "status": "ACCEPTED",
  "acceptedAt": "2026-07-31T09:00:00.000Z"
}
```

**Response 400 Bad Request** — 유효성 검증 실패, Kafka 미반영

```json
{
  "errorCode": "INVALID_PRICE_UNIT",
  "message": "가격이 KRW-BTC의 호가 단위(1,000원)의 배수가 아닙니다.",
  "requestId": "..."
}
```

**Response 429 Too Many Requests** — 컨슈머 랙 기준치 초과로 즉시 거절(NFR-13)

```json
{
  "errorCode": "CONSUMER_LAG_EXCEEDED",
  "message": "현재 시스템 처리 한계를 초과하여 주문을 거절합니다.",
  "requestId": "..."
}
```

| 항목 | 검증 기준 |
|---|---|
| 정상 주문 | HTTP 202 + 주문 번호 반환, Kafka `orders` 토픽에서 조회 가능 |
| 잘못된 주문 | HTTP 400 + 오류 코드 반환, Kafka 미반영 |
| 과부하 상태 | 컨슈머 랙이 기준치 초과 시 신규 요청 HTTP 429 즉시 거절 |

### 2.2 중복 주문 방지 (멱등성)

동일 `Idempotency-Key`로 재요청 시 재검증·재발행 없이 최초 응답을 그대로 반환한다(FR-02).

| 항목 | 검증 기준 |
|---|---|
| 동일 `Idempotency-Key`로 2회 요청 | 응답(HTTP 상태·본문)이 최초 요청과 동일, Kafka에는 1건만 반영 |
| 키 미전달 | HTTP 400, `errorCode: MISSING_IDEMPOTENCY_KEY` |

### 2.3 주문 취소 접수
`DELETE /v1/orders/{orderId}`

미체결 주문의 취소 요청을 접수한다(FR-03). 취소 시 잔여 수량이 호가창에서 제거되며(FR-10), 전량 체결된 주문의 취소는 거절한다.

**Response 200 OK**

```json
{
  "orderId": "ord_20260731_0000001",
  "status": "CANCELED",
  "canceledQuantity": "0.015",
  "canceledAt": "2026-07-31T09:05:00.000Z"
}
```

**Response 409 Conflict** — 전량 체결된 주문의 취소 요청

```json
{
  "errorCode": "ORDER_ALREADY_FILLED",
  "message": "이미 전량 체결된 주문은 취소할 수 없습니다.",
  "requestId": "..."
}
```

**Response 404 Not Found** — 존재하지 않는 `orderId`

```json
{
  "errorCode": "ORDER_NOT_FOUND",
  "message": "해당 주문을 찾을 수 없습니다.",
  "requestId": "..."
}
```

| 항목 | 검증 기준 |
|---|---|
| 미체결 주문 취소 | 잔여 수량이 호가창에서 제거됨 |
| 전량 체결 주문 취소 | HTTP 409로 거절 |

---

## 3. 조회 API

### 3.1 호가창 조회
`GET /v1/markets/{market}/orderbook`

마켓별 호가창을 조회한다(FR-12). 매수는 고가순, 매도는 저가순으로 정렬해 반환한다.

**Query Parameters**
| 파라미터 | 필수 | 설명 |
|---|---|---|
| `depth` | N | 반환할 호가 단계 수 (기본값 20, 최대 100) |

**Response 200 OK**

```json
{
  "market": "KRW-BTC",
  "timestamp": "2026-07-31T09:00:00.000Z",
  "bids": [
    { "price": "71500000", "quantity": "0.320" },
    { "price": "71490000", "quantity": "0.150" }
  ],
  "asks": [
    { "price": "71510000", "quantity": "0.210" },
    { "price": "71520000", "quantity": "0.400" }
  ]
}
```

| 항목 | 검증 기준 |
|---|---|
| 정렬 순서 | 매수(`bids`) 고가순, 매도(`asks`) 저가순 |
| 정합성 | 조회 결과가 매칭 엔진의 실제 상태와 일치 |
| 응답 시간 | p95 50밀리초 이하(NFR-04) |

### 3.2 거래 내역 조회 (선택, FR-13)
`GET /v1/markets/{market}/trades`

최근 순으로 거래 내역을 조회한다. 누락·중복 없는 페이지 이동을 위해 커서 기반 페이지네이션을 사용한다.

**Query Parameters**
| 파라미터 | 필수 | 설명 |
|---|---|---|
| `cursor` | N | 이전 응답의 `nextCursor` 값. 미전달 시 최신 내역부터 조회 |
| `limit` | N | 페이지당 건수 (기본값 50, 최대 200) |

**Response 200 OK**

```json
{
  "market": "KRW-BTC",
  "trades": [
    {
      "tradeId": "trd_20260731_0000098",
      "buyOrderId": "ord_20260731_0000001",
      "sellOrderId": "ord_20260731_0000002",
      "price": "71500000",
      "quantity": "0.015",
      "executedAt": "2026-07-31T09:04:58.000Z"
    }
  ],
  "nextCursor": "trd_20260731_0000097"
}
```

| 항목 | 검증 기준 |
|---|---|
| 페이지 이동 | 커서 기반 조회 시 누락·중복 없음 |

---

## 4. 공통 오류 응답

**오류 응답 포맷**

```json
{
  "errorCode": "STRING",
  "message": "STRING",
  "requestId": "STRING"
}
```

**오류 코드 총괄표**

| errorCode | HTTP 상태 | 발생 API | 설명 |
|---|---|---|---|
| `INVALID_MARKET` | 400 | 주문 접수 | 대상 20개 마켓에 없는 마켓 코드 |
| `INVALID_SIDE` | 400 | 주문 접수 | `BUY`/`SELL` 이외의 값 |
| `INVALID_PRICE` | 400 | 주문 접수 | 가격이 0 이하이거나 형식 오류 |
| `INVALID_PRICE_UNIT` | 400 | 주문 접수 | 가격이 마켓별 호가 단위의 배수가 아님 |
| `INVALID_QUANTITY` | 400 | 주문 접수 | 수량이 0 이하이거나 형식 오류 |
| `MISSING_IDEMPOTENCY_KEY` | 400 | 주문 접수 | `Idempotency-Key` 헤더 누락 |
| `CONSUMER_LAG_EXCEEDED` | 429 | 주문 접수 | 컨슈머 랙 기준치 초과로 즉시 거절(NFR-13) |
| `ORDER_NOT_FOUND` | 404 | 주문 취소 | 존재하지 않는 `orderId` |
| `ORDER_ALREADY_FILLED` | 409 | 주문 취소 | 전량 체결된 주문의 취소 요청 |
| `MARKET_NOT_FOUND` | 404 | 조회 | 존재하지 않는 마켓 코드 |
| `INTERNAL_ERROR` | 500 | 전체 | 서버 내부 오류 |

---

## 5. 데이터 타입 정의

| 타입 | 값 | 설명 |
|---|---|---|
| `market` | `KRW-USDT`, `KRW-BTC`, `KRW-XRP`, `KRW-ETH`, `KRW-ONDO`, `KRW-LA`, `KRW-SHIB`, `KRW-RE`, `KRW-DOGE`, `KRW-SLX`, `KRW-KAITO`, `KRW-SOL`, `KRW-XLM`, `KRW-WLD`, `KRW-MIRA`, `KRW-ERA`, `KRW-ADA`, `KRW-AI`, `KRW-NEAR`, `KRW-ARX` | 요구사항 정의서 1.1.4 대상 종목 20개와 동일 |
| `side` | `BUY`, `SELL` | 매수/매도 |
| `order.status` | `ACCEPTED`, `PARTIALLY_FILLED`, `FILLED`, `CANCELED` | 주문 상태 |
| `price`, `quantity` | 문자열(십진수) | 부동소수점 오차 방지를 위해 문자열로 직렬화 |

> 마켓별 호가 단위(tick size) 값은 정책 변경 가능성이 있어 별도 설정 테이블로 관리하며, 본 문서에서는 `INVALID_PRICE_UNIT` 검증 대상임만 명시한다.

---

## 6. 규격 관리 (FR-22)

- 본 문서는 저장소에서 버전 관리하며, 변경 시 변경 이력 표에 기록한다.
- 실제 API 응답과 본 문서 간 자동 검사(계약 테스트)를 CI에 구성해, 규격과 어긋나는 변경은 배포를 차단한다(FR-22, FR-24).
- B(매칭 엔진), C(트레이더·리플레이 엔진)는 본 API를 호출하는 클라이언트 관점에서 이 문서를 참조하며, A는 매칭 엔진 구현 없이 본 문서 기준 Mock으로 자체 시험한다(FR-23).

---

## 7. 향후 검토 사항

- FR-25 모니터링 화면의 실시간 반영(호가창·처리량 추이)은 폴링 방식의 REST 조회만으로는 지연이 발생할 수 있어, WebSocket/SSE 스트리밍 API 도입 여부를 별도로 검토한다.
- FR-21의 "주문 번호 검색 시 구간별 소요 시간 조회"는 분산 트레이싱 시스템(예: Jaeger) 조회로 대체 가능하며, 커스텀 REST 엔드포인트 필요 여부는 C팀과 협의해 확정한다.
