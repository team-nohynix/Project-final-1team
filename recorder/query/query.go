// Package query는 recorder가 RDS에 이미 쌓아둔 TRADE_ORDER/EXECUTION/
// MATCHING_ENGINE_ASSIGNMENT를 읽기만 하는 조회 전용 계층입니다. store 패키지
// (쓰기 전용, apply.go가 씀)와는 분리합니다 — 읽기와 쓰기가 서로의 트랜잭션/
// 배치 로직에 영향을 주지 않게 하려는 것뿐, 특별한 이유가 있어서 나눈 건
// 아닙니다. server.go의 HTTP 핸들러가 이 Querier 인터페이스만 알고, 실제
// MySQL 구현(MySQLQuerier)은 몰라도 됩니다 — store.Store/kafkaclient.Publisher와
// 같은 테스트 패턴(핸들러는 가짜 구현체로 테스트).
package query

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"time"

	"recorder/store"
)

// tpsWindow/p99Window/seriesWindow/seriesBucket은 DashboardMetrics의 계산
// 창입니다 — 실측 후 조정할 잠정값입니다(backpressure 워터마크와 같은 성격).
// TimeWindow는 DashboardMetrics의 TPS/p99 계산 구간을 기본 롤링 창 대신
// 명시적으로 지정할 때 씁니다 — Querier.DashboardMetrics 주석 참고.
type TimeWindow struct {
	From time.Time
	To   time.Time
}

const (
	tpsWindow       = 60 * time.Second
	p99Window       = 5 * time.Minute
	seriesWindow    = 10 * time.Minute
	seriesBucket    = time.Minute
	seriesBucketFmt = "2006-01-02T15:04:00Z" // 분 단위로 잘라 표시(초는 항상 00)

	// ordersByStatusWindow은 OrdersByStatus 전용 창입니다(seriesWindow과
	// 별개). 2026-08-21에 "세션 전체 누적처럼 보이게" 24시간으로 늘렸다가
	// 몇 분 만에 RDS CPU가 트래픽 0인 상태에서 13%→57%로 튀는 걸 실측하고
	// 되돌렸습니다 — "인덱스를 타니 range가 커도 비용은 그 구간 행 수에만
	// 비례한다"는 이유 자체는 맞지만, 이 시스템의 실제 처리량(테스트 중
	// 100건/초 이상)에서는 "24시간 구간"이 사실상 "오늘 하루 전체"와
	// 맞먹는 행 수라서 예전 무제한 GROUP BY와 실질적으로 비슷한 규모의
	// 스캔이 됐습니다. "인덱스로 bound돼 있다"가 아니라 "그 구간의 실제
	// 행 수가 충분히 작다"가 진짜 안전 조건입니다. 세션 전체 누적을
	// 정확히 보여주려면 recorder가 실제 세션 시작 시각을 알아야 하는데
	// (지금은 모름) 그건 별도 작업이라, 우선 안전했던 10분으로 되돌립니다.
	ordersByStatusWindow = seriesWindow
)

// ThroughputMaxRange은 GET /v1/metrics/throughput(2026-08-24, Grafana처럼
// 처리량 차트 기간을 직접 고르는 기능)이 허용하는 최대 구간입니다 — 위
// ordersByStatusWindow 주석의 2026-08-21 사고(구간을 10분→24시간으로
// 늘렸다가 트래픽 0인데도 RDS CPU가 13%→57%로 튐 — "인덱스를 타도 결국
// 그 구간의 실제 행 수만큼은 스캔한다"는 교훈)와 같은 이유로 상한을 둡니다.
// server.go의 throughputHandler가 이 값으로 요청을 검증합니다(400) — 여기
// 그대로 두면 검증 실패와 진짜 쿼리 실패(500)가 같은 에러 경로로 섞입니다.
// 다만 상한을 두는 것만으론 부족합니다 — 이 엔드포인트는
// dashboardMetricsHandler의 기본 10분 창(Redis 캐시, 10초 폴링)과 달리
// 캐시가 없는 라이브 쿼리라, 프론트가 이 엔드포인트를 자동 폴링하면
// 안 됩니다(server.go throughputHandler / DashboardView.vue 주석 참고).
const ThroughputMaxRange = 24 * time.Hour

// ExecutionTrace는 OrderTrace.Executions의 항목 하나입니다.
type ExecutionTrace struct {
	ExecutionID string `json:"executionId"`
	Market      string `json:"market"`
	BuyOrderID  string `json:"buyOrderId"`
	SellOrderID string `json:"sellOrderId"`
	Price       string `json:"price"`
	Quantity    string `json:"quantity"`
	Mode        string `json:"mode,omitempty"`
	ExecutedAt  string `json:"executedAt"`
}

// OrderTrace는 GET /v1/trace/{orderId}의 응답입니다. recorder가 실제로 갖고
// 있는 시각은 주문 접수(SubmittedAt)/체결(각 Execution의 ExecutedAt)/취소
// (CanceledAt)뿐입니다 — "API 접수 → Kafka 적재 → 매칭 완료 → 체결 결과 발행 →
// PostgreSQL" 같은 세분화된 5단계 파이프라인 타임스탬프는 어디에도 저장되지
// 않으므로 그 형태로는 응답하지 않습니다(docs/frontend-backend-integration.md
// 3.2 참고 — 이 부분은 프론트 쪽 화면을 실제 데이터에 맞게 다시 설계해야 함).
type OrderTrace struct {
	OrderID           string           `json:"orderId"`
	Market            string           `json:"market"`
	Side              string           `json:"side"`
	Price             string           `json:"price"`
	Quantity          string           `json:"quantity"`
	RemainingQuantity string           `json:"remainingQuantity"`
	Status            string           `json:"status"`
	Mode              string           `json:"mode"`
	SubmittedAt       string           `json:"submittedAt"`
	CanceledAt        string           `json:"canceledAt,omitempty"`
	SourceOrderID     string           `json:"sourceOrderId,omitempty"`
	Executions        []ExecutionTrace `json:"executions"`
}

// MarketAssignment는 EngineAssignment.Markets의 항목 하나입니다.
type MarketAssignment struct {
	Market     string `json:"market"`
	AssignedAt string `json:"assignedAt"`
}

// EngineAssignment는 GET /v1/matching/engines의 항목 하나 — 엔진 인스턴스
// 하나가 지금 담당 중인 마켓 목록입니다.
type EngineAssignment struct {
	EngineInstanceID string             `json:"engineInstanceId"`
	Markets          []MarketAssignment `json:"markets"`
}

// Querier는 조회 전용 인터페이스입니다.
type Querier interface {
	// TraceOrder는 주문 하나의 현재 상태와 그 주문이 관련된 체결 내역을
	// 반환합니다. 그런 주문이 없으면 found=false(에러 아님).
	TraceOrder(ctx context.Context, orderID string) (trace OrderTrace, found bool, err error)
	// ListEngines는 지금 담당 중인(released_at IS NULL) 배정만 모아 엔진별로
	// 묶어 반환합니다 — 크래시 등으로 반납을 못 받은 오래된 배정도 이 목록에
	// 그대로 나타날 수 있습니다(store.AssignMarket이 이미 그런 경우를
	// self-heal하지만, 마지막 배정이 열린 채로 남는 짧은 창은 있을 수 있음).
	ListEngines(ctx context.Context) ([]EngineAssignment, error)
	// DashboardMetrics는 프론트 DashboardView가 필요로 하는 실시간 지표
	// (docs/frontend-backend-integration.md 3.1)를 recorder가 이미 RDS에
	// 쌓아둔 데이터에서 계산해 반환합니다. window가 nil이면 기본 동작(TPS는
	// 최근 tpsWindow, p99는 최근 p99Window의 롤링 창)입니다 — 진행 중인
	// 테스트가 있을 때(방금 몇 초/분 안의 실시간 처리량이 의미 있을 때)
	// 씁니다. window가 주어지면(2026-08-25, "진행 중인 테스트가 없을 땐
	// 최근 실행 통계를 보여주는 게 낫다"는 요청 지원) TPS/p99를 그 [From,To)
	// 구간 전체로 계산합니다 — TPS는 그 구간 전체 평균(카운트 ÷ 구간
	// 길이)이 됩니다. 호출부(metrics.go의 poller)가 세션 상태를 보고 결정합니다.
	DashboardMetrics(ctx context.Context, window *TimeWindow) (DashboardMetrics, error)
	// OrderSummary는 mode+[from,to) 구간에 접수된 주문을 상태별로 집계합니다 —
	// 프론트의 "페이퍼 트레이딩 실행 결과" 화면(접수/체결/미체결 수, 2026-08-12)을
	// 지원합니다. to가 zero value면 지금 시각을 씁니다(아직 IN_PROGRESS인 실행의
	// 현재까지 집계를 보여줄 때 씀). 이 구간 지정이 정확한 이유는 orderapi의
	// 세션 가드가 트레이더/리플레이 엔진을 동시에 하나만 실행되게 막아서,
	// [from,to) 구간에 다른 실행의 주문이 섞일 수 없기 때문입니다.
	OrderSummary(ctx context.Context, mode string, from, to time.Time) (OrderSummary, error)
	// UnresolvedOrders는 mode+[from,to) 구간에서 아직 안 끝난(ACCEPTED/
	// PARTIALLY_FILLED) 주문의 ID+마켓 목록을 반환합니다 — orderapi가 세션
	// 종료 시점에 그 세션이 남긴 미종결 주문을 취소하는 데 씁니다(2026-08-19,
	// 부하테스트 반복으로 매칭 엔진 인메모리 오더북에 미체결 주문이 계속
	// 쌓여 OOMKilled까지 간 사고 대응). OrderSummary와 같은 이유로 구간
	// 지정만으로 정확합니다(세션 가드가 겹침을 막음) — 별도 세션 식별 컬럼
	// 없이도 이 세션 소속 주문을 정확히 구분할 수 있습니다.
	UnresolvedOrders(ctx context.Context, mode string, from, to time.Time) ([]UnresolvedOrder, error)
	// AllUnresolvedOrders는 mode/기간 제한 없이 지금 ACCEPTED/PARTIALLY_FILLED
	// 상태인 주문 전부를 반환합니다. UnresolvedOrders와 달리 "이번에 막 끝난
	// 세션 하나"가 아니라 "과거 여러 세션이 누적으로 남긴 백로그 전체"를 한
	// 번에 정리하는 용도(2026-08-20, 프론트 수동 정리 버튼)라 세션 가드가
	// 보장하는 구간 분리가 필요 없습니다 — 그냥 지금 시점의 미종결 전부입니다.
	AllUnresolvedOrders(ctx context.Context) ([]UnresolvedOrder, error)
	// ThroughputSeries는 GET /v1/metrics/throughput(2026-08-24, "주문·체결
	// 처리량" 차트의 기간을 Grafana처럼 직접 고르는 기능)이 씁니다.
	// DashboardMetrics.Series(고정 10분/1분, 캐시됨)와 달리 임의의 [from, to)
	// 구간을 라이브로 계산합니다 — throughputMaxRange 주석 참고.
	ThroughputSeries(ctx context.Context, from, to time.Time) ([]MetricsBucket, error)
	// IntegrityCheck는 mode+[from,to) 구간의 데이터 정합성을 검사합니다 —
	// "시스템 종합 현황" 대시보드의 "데이터 정합성 검사" 패널(2026-08-24)
	// 지원. "주문 유실"은 여기 없음 — orderapi의 GET /v1/jobs/replay-preview
	// 의 totalOrders(예정된 주문량)와 OrderSummary.Accepted(실제 접수량)의
	// 차이로 프론트가 직접 계산한다(replayengine은 429 등을 재시도 없이
	// 스킵하므로, 이 차이가 곧 스킵된 주문 수 — DB 조회가 필요 없음).
	IntegrityCheck(ctx context.Context, mode string, from, to time.Time) (IntegrityCheck, error)
}

// UnresolvedOrder는 UnresolvedOrders 응답의 항목 하나입니다. Market은
// orderapi가 PublishCancel(Kafka 파티션 키)에 필요해서 같이 줍니다.
type UnresolvedOrder struct {
	OrderID string `json:"orderId"`
	Market  string `json:"market"`
}

// OrderSummary는 GET /v1/orders/summary의 응답입니다. Accepted는 그 구간에
// 접수된 전체 주문 수(모든 상태 포함), Filled는 그중 FILLED, Unfilled는
// ACCEPTED/PARTIALLY_FILLED(아직 안 끝난 것)입니다 — CANCELED는 셋 다에
// 안 들어가고 Accepted에만 포함되므로, Filled+Unfilled가 Accepted보다
// 작을 수 있습니다(취소된 만큼).
type OrderSummary struct {
	Accepted int64 `json:"accepted"`
	Filled   int64 `json:"filled"`
	Unfilled int64 `json:"unfilled"`
	// ByMarket/BySide는 2026-08-20 추가 — 페이퍼 트레이딩 "실행 결과" 화면에
	// 마켓별 개수/체결률, 매수·매도 비율을 보여달라는 요청 지원. 비율 자체는
	// 여기서 계산하지 않고 원본 개수만 내려준다 — 프론트가 나누기만 하면
	// 되는 값을 서버가 미리 계산해주지 않는 이 프로젝트의 기존 관례(실행
	// 시간을 endedAt-startedAt으로 프론트가 계산하는 것과 동일한 이유)와
	// 같다. "생성된 주문 수"는 논의 끝에 Accepted를 그대로 쓰기로 했다 —
	// 봇의 원시 판단 횟수나 거절된 주문 수는 여전히 구조적으로 관측
	// 불가능하다(CLAUDE.md 참고).
	ByMarket []MarketOrderSummary `json:"byMarket"`
	BySide   []SideOrderSummary   `json:"bySide"`
}

// MarketOrderSummary는 OrderSummary.ByMarket의 항목 하나 — 마켓 하나의
// accepted/filled/unfilled입니다(최상위 OrderSummary와 같은 세 필드,
// 프론트가 같은 렌더링 로직을 마켓별 행에도 재사용할 수 있도록).
type MarketOrderSummary struct {
	Market   string `json:"market"`
	Accepted int64  `json:"accepted"`
	Filled   int64  `json:"filled"`
	Unfilled int64  `json:"unfilled"`
}

// SideOrderSummary는 OrderSummary.BySide의 항목 하나 — "BUY"/"SELL" 각각의
// 접수 건수입니다.
type SideOrderSummary struct {
	Side  string `json:"side"`
	Count int64  `json:"count"`
}

// IntegrityCheck는 GET /v1/orders/integrity의 응답입니다 — "시스템 종합
// 현황" 대시보드의 "데이터 정합성 검사" 패널 지원(2026-08-24). 세 지표 다
// 0이면 정상입니다.
type IntegrityCheck struct {
	// DuplicateExecutions는 한 주문에 대해 체결된 수량 합이 원래 주문
	// 수량을 넘는 경우의 건수입니다 — Kafka at-least-once 재전달이 이중
	// 반영됐거나 매칭 로직에 버그가 있으면 잡힙니다.
	DuplicateExecutions int64 `json:"duplicateExecutions"`
	// SequenceReversals는 체결 시각이 그 체결에 관련된 주문(매수 또는 매도)의
	// 접수 시각보다 이른(물리적으로 불가능한) 경우의 건수입니다.
	SequenceReversals int64 `json:"sequenceReversals"`
	// BuyFilled/SellFilled는 각각 매수/매도 주문의 (quantity-remaining_quantity)
	// 합계(=그 구간에 실제로 체결된 양)입니다. 매칭은 항상 매수 하나-매도
	// 하나를 정확히 같은 양만큼 짝지어 채우므로(FR-06), 이론적으로 두 값은
	// 항상 같아야 합니다 — 어긋나면 한쪽 주문에만 체결이 반영되고 반대쪽엔
	// 안 됐거나(주문 유실과 같은 근본 원인일 수 있음), applyFillsBatch 로직
	// 자체에 문제가 있다는 신호입니다. 다만 이전 세션이 남긴 잔존 주문이
	// 이번 세션 주문과 매칭된 드문 경우엔 세션 경계에 걸려 정상적으로도
	// 어긋날 수 있습니다(ResolveMode의 mode-mismatch와 같은 성격의 노이즈) —
	// 그래서 0이 아니라고 무조건 심각한 버그는 아니고, 일단 로그를 봐야 하는
	// 신호로 다루는 게 맞습니다. 문자열인 이유는 price/quantity와 동일(DECIMAL
	// 정밀도를 JSON 숫자 왕복에서 잃지 않기 위함).
	BuyFilled  string `json:"buyFilled"`
	SellFilled string `json:"sellFilled"`
}

// MetricsBucket은 DashboardMetrics.Series의 항목 하나 — 1분 단위 버킷의
// 접수/체결 건수입니다. 프론트의 "주문·체결 처리량 라인 차트"용입니다.
type MetricsBucket struct {
	BucketStart string `json:"bucketStart"`
	Orders      int64  `json:"orders"`
	Executions  int64  `json:"executions"`
}

// DashboardMetrics는 GET /v1/metrics/dashboard의 응답입니다.
//
// 정의(다소 자의적일 수 있어 명시): OrderAcceptTps/ExecutionTps는 최근
// tpsWindow(60초) 동안의 접수/체결 건수를 60으로 나눈 순간 평균입니다.
// PendingOrders는 아직 종결(FILLED/CANCELED)되지 않은 주문 수(ACCEPTED +
// PARTIALLY_FILLED)의 스냅샷입니다. E2EP99Ms는 최근 p99WindowMinutes(5분)
// 안에 일어난 체결(execution)마다 "그 체결을 촉발한 쪽(taker — 매수·매도
// 주문 중 더 늦게 접수된 쪽)의 접수 시각 → executed_at" 구간을 표본으로
// 계산합니다(2026-08-26 재설계). taker는 도착하자마자 이미 있던 반대편
// 주문과 즉시 매칭되므로 이 구간엔 "시장에서 기다린 시간"이 섞이지
// 않습니다 — 예전엔 FILLED된 "주문" 각각의 자기 접수 시각을 썼는데, 그러면
// 오래 대기하다 체결된 수동적(maker) 주문의 대기 시간까지 그대로 p99에
// 섞여 NFR-03(500ms)과 비교가 안 되는 값(실측 100초대)이 나왔습니다. 여전히
// 완벽한 "매칭 시작/큐 대기" 단계별 지연은 아닙니다(현재 스키마에 그런
// 중간 타임스탬프가 없음, recorder/server.go traceHandler 주석의 5단계
// 파이프라인 부재와 같은 근본 원인) — 하지만 시장 대기 시간을 배제한
// 근사치로는 이전보다 훨씬 낫습니다. RunningEnginePods는 ListEngines
// 결과의 distinct engineInstanceId 수를 그대로 씁니다.
type DashboardMetrics struct {
	OrderAcceptTps    float64 `json:"orderAcceptTps"`
	ExecutionTps      float64 `json:"executionTps"`
	PendingOrders     int64   `json:"pendingOrders"`
	E2EP99Ms          float64 `json:"e2eP99Ms"`
	E2EP99SampleCount int     `json:"e2eP99SampleCount"`
	RunningEnginePods int     `json:"runningEnginePods"`
	// TpsWindowSource/E2EWindowSource는 2026-08-25 추가 — OrderAcceptTps/
	// ExecutionTps/E2EP99Ms가 "최근 롤링 창"과 "마지막 실행 구간" 중 어느
	// 쪽으로 계산됐는지(DashboardMetrics(ctx, window) 주석 참고)를 프론트가
	// 그대로 표시할 수 있게 합니다 — "realtime"(최근 tpsWindow/p99Window)
	// 또는 "last_run"(진행 중인 테스트가 없어 마지막 실행 [시작,종료) 구간
	// 전체로 계산). 지금은 TPS 둘, E2E P99 하나가 항상 같은 window를
	// 공유해서 값이 서로 같지만, 필드를 나눠 두면 나중에 서로 다른 소스로
	// 갈라져도 API를 안 깨도 됩니다.
	TpsWindowSource string           `json:"tpsWindowSource"`
	E2EWindowSource string           `json:"e2eWindowSource"`
	Series          []MetricsBucket  `json:"series"`
	OrdersByStatus  map[string]int64 `json:"ordersByStatus"`
}

// MySQLQuerier는 Querier를 실제 MySQL(RDS)로 구현합니다. store/mysql.go와
// 같은 이유로 이 파일은 단위 테스트하지 않고 손으로 검증합니다 — 실제 DB
// 왕복이 핵심이라 단위 테스트로는 의미 있게 못 검증합니다.
type MySQLQuerier struct {
	db *sql.DB
}

func NewMySQLQuerier(db *sql.DB) *MySQLQuerier {
	return &MySQLQuerier{db: db}
}

func (q *MySQLQuerier) TraceOrder(ctx context.Context, orderID string) (OrderTrace, bool, error) {
	var (
		trace         OrderTrace
		submittedAt   sql.NullTime
		canceledAt    sql.NullTime
		sourceOrderID sql.NullString
	)
	err := q.db.QueryRowContext(ctx, `
		SELECT order_id, market_code, side, price, quantity, remaining_quantity, status, mode, submitted_at, canceled_at, source_order_id
		FROM trade_order WHERE order_id = ?
	`, orderID).Scan(
		&trace.OrderID, &trace.Market, &trace.Side, &trace.Price, &trace.Quantity,
		&trace.RemainingQuantity, &trace.Status, &trace.Mode, &submittedAt, &canceledAt, &sourceOrderID,
	)
	if err == sql.ErrNoRows {
		return OrderTrace{}, false, nil
	}
	if err != nil {
		return OrderTrace{}, false, fmt.Errorf("주문 조회 실패 (orderId=%s): %w", orderID, err)
	}
	trace.SubmittedAt = formatNullTime(submittedAt)
	trace.CanceledAt = formatNullTime(canceledAt)
	trace.SourceOrderID = sourceOrderID.String

	rows, err := q.db.QueryContext(ctx, `
		SELECT execution_id, market_code, buy_order_id, sell_order_id, price, quantity, mode, executed_at
		FROM execution WHERE buy_order_id = ? OR sell_order_id = ?
		ORDER BY executed_at ASC
	`, orderID, orderID)
	if err != nil {
		return OrderTrace{}, false, fmt.Errorf("체결 내역 조회 실패 (orderId=%s): %w", orderID, err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			ex         ExecutionTrace
			mode       sql.NullString
			executedAt sql.NullTime
		)
		if err := rows.Scan(&ex.ExecutionID, &ex.Market, &ex.BuyOrderID, &ex.SellOrderID, &ex.Price, &ex.Quantity, &mode, &executedAt); err != nil {
			return OrderTrace{}, false, fmt.Errorf("체결 내역 조회 실패 (orderId=%s): %w", orderID, err)
		}
		ex.Mode = mode.String
		ex.ExecutedAt = formatNullTime(executedAt)
		trace.Executions = append(trace.Executions, ex)
	}
	if err := rows.Err(); err != nil {
		return OrderTrace{}, false, fmt.Errorf("체결 내역 조회 실패 (orderId=%s): %w", orderID, err)
	}

	return trace, true, nil
}

func (q *MySQLQuerier) ListEngines(ctx context.Context) ([]EngineAssignment, error) {
	rows, err := q.db.QueryContext(ctx, `
		SELECT engine_instance_id, market_code, assigned_at
		FROM matching_engine_assignment
		WHERE released_at IS NULL
		ORDER BY engine_instance_id, market_code
	`)
	if err != nil {
		return nil, fmt.Errorf("엔진 배정 목록 조회 실패: %w", err)
	}
	defer rows.Close()

	// engine_instance_id 순서대로 나오므로(ORDER BY), 등장 순서를 그대로 결과
	// 순서로 써도 결정적입니다 — 별도 정렬이 필요 없습니다.
	byEngine := make(map[string]*EngineAssignment)
	var order []string
	for rows.Next() {
		var (
			engineID   string
			market     string
			assignedAt sql.NullTime
		)
		if err := rows.Scan(&engineID, &market, &assignedAt); err != nil {
			return nil, fmt.Errorf("엔진 배정 목록 조회 실패: %w", err)
		}
		e, ok := byEngine[engineID]
		if !ok {
			e = &EngineAssignment{EngineInstanceID: engineID}
			byEngine[engineID] = e
			order = append(order, engineID)
		}
		e.Markets = append(e.Markets, MarketAssignment{Market: market, AssignedAt: formatNullTime(assignedAt)})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("엔진 배정 목록 조회 실패: %w", err)
	}

	out := make([]EngineAssignment, 0, len(order))
	for _, id := range order {
		out = append(out, *byEngine[id])
	}
	return out, nil
}

func formatNullTime(t sql.NullTime) string {
	if !t.Valid {
		return ""
	}
	return t.Time.UTC().Format(store.ISOLayout)
}

func (q *MySQLQuerier) DashboardMetrics(ctx context.Context, window *TimeWindow) (DashboardMetrics, error) {
	var m DashboardMetrics
	if window != nil {
		m.TpsWindowSource = "last_run"
		m.E2EWindowSource = "last_run"
	} else {
		m.TpsWindowSource = "realtime"
		m.E2EWindowSource = "realtime"
	}

	// window가 있으면(진행 중인 테스트가 없어 "최근 실행" 구간을 통째로
	// 보여줄 때) 그 [From,To) 구간 전체 카운트 ÷ 구간 길이를 평균 TPS로
	// 쓴다 — 없으면(기본) 지금까지처럼 최근 tpsWindow(1분) 롤링 창.
	tpsSeconds := tpsWindow.Seconds()
	var acceptCount, execCount, pendingCount int64
	if window != nil {
		tpsSeconds = window.To.Sub(window.From).Seconds()
		if tpsSeconds <= 0 {
			tpsSeconds = 1 // 0으로 나누기 방지 — 시작·종료가 같은 순간이면(사실상 없던 실행) TPS는 그냥 0이 되게
		}
		if err := q.db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM trade_order WHERE submitted_at >= ? AND submitted_at < ?`,
			window.From, window.To,
		).Scan(&acceptCount); err != nil {
			return DashboardMetrics{}, fmt.Errorf("접수 TPS 조회 실패: %w", err)
		}
		if err := q.db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM execution WHERE executed_at >= ? AND executed_at < ?`,
			window.From, window.To,
		).Scan(&execCount); err != nil {
			return DashboardMetrics{}, fmt.Errorf("체결 TPS 조회 실패: %w", err)
		}
	} else {
		if err := q.db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM trade_order WHERE submitted_at >= UTC_TIMESTAMP() - INTERVAL ? SECOND`,
			int(tpsWindow.Seconds()),
		).Scan(&acceptCount); err != nil {
			return DashboardMetrics{}, fmt.Errorf("접수 TPS 조회 실패: %w", err)
		}
		if err := q.db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM execution WHERE executed_at >= UTC_TIMESTAMP() - INTERVAL ? SECOND`,
			int(tpsWindow.Seconds()),
		).Scan(&execCount); err != nil {
			return DashboardMetrics{}, fmt.Errorf("체결 TPS 조회 실패: %w", err)
		}
	}
	if err := q.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM trade_order WHERE status IN ('ACCEPTED', 'PARTIALLY_FILLED')`,
	).Scan(&pendingCount); err != nil {
		return DashboardMetrics{}, fmt.Errorf("처리 대기 주문 조회 실패: %w", err)
	}
	m.OrderAcceptTps = float64(acceptCount) / tpsSeconds
	m.ExecutionTps = float64(execCount) / tpsSeconds
	m.PendingOrders = pendingCount

	// 상태별 건수(recorder_orders_by_status, Grafana "주문 상태별 건수 추이"
	// 패널) — 2026-08-19에 처음 추가했다가 status만으로 GROUP BY(시간 제한
	// 없음)해서 RDS CPU 97~99% 포화 사고의 원인 중 하나가 돼 2026-08-21에
	// 통째로 뺐던 지표입니다. FILLED/CANCELED는 종결 후에도 행이 영원히
	// 남아 전체 COUNT가 시간이 갈수록 무한히 커지는데, 그 전체를 매번
	// GROUP BY로 다시 세는 게 문제였습니다. 여기서는 idx_trade_order_status_submitted
	// (status, submitted_at) 인덱스를 그대로 타도록 상태 4개를 각각
	// "최근 ordersByStatusWindow(10분) 안에 접수된 주문 중 그 상태" 쿼리로
	// 나눠서, 사고 전과 달리 테이블 전체 크기와 무관하게 그 시간대 행 수에만
	// 비례하는 비용으로 만들었습니다(E2E P99 쿼리를 나눈 것과 같은 이유).
	// (같은 날 24시간으로 늘렸다가 트래픽 0인데도 RDS CPU가 57%로 튀는 걸
	// 보고 즉시 되돌린 적이 있습니다 — ordersByStatusWindow 상수 주석 참고.)
	statusByStatus := map[string]int64{}
	for _, status := range []string{"ACCEPTED", "PARTIALLY_FILLED", "FILLED", "CANCELED"} {
		var count int64
		if err := q.db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM trade_order WHERE status = ? AND submitted_at >= UTC_TIMESTAMP() - INTERVAL ? MINUTE`,
			status, int(ordersByStatusWindow.Minutes()),
		).Scan(&count); err != nil {
			return DashboardMetrics{}, fmt.Errorf("상태별 건수 조회 실패 (status=%s): %w", status, err)
		}
		statusByStatus[status] = count
	}
	m.OrdersByStatus = statusByStatus

	// E2E P99(taker 기준, 2026-08-26 재설계, 같은 날 두 번째 개정): 최근
	// p99Window 안에 일어난 체결(execution)마다 GREATEST(매수 접수 시각,
	// 매도 접수 시각) → executed_at 구간을 표본으로 삼는다 — DashboardMetrics
	// 타입 주석 참고. "체결"을 표본 단위로 삼으므로(예전처럼 "주문" 단위가
	// 아님) 표본 하나당 관련 주문이 최대 둘(매수/매도)뿐이라 taker/maker를
	// 섞을 일이 없다.
	//
	// **두 번째 개정 이유**: 최초 재설계는 "execution을 시간으로 거른 뒤,
	// 관련 주문들의 submitted_at을 trade_order PK로 별도 배치(IN 절, 최대
	// 5000개씩 청크) 조회"하는 2단계 방식이었다 — OR-JOIN("trade_order o
	// JOIN execution e ON (e.buy_order_id = o.order_id OR e.sell_order_id =
	// o.order_id)"가 execution 전체(500만행+)를 스캔했던 사고, 2026-08-20
	// EXPLAIN으로 rows:5,063,699 확인)을 피하려던 것이었다. 그런데 실측 부하
	// 시험(2026-08-26, 초당 백여 건대 체결)에서 5분 창에 관련 주문이 수만~
	// 수십만 개까지 쌓이면서, 청크 하나당 왕복 하나씩(최대 5000개/청크) 수십
	// 번 왕복이 필요해졌고, 그 누적 지연이 recorder-api의 요청 컨텍스트
	// 타임아웃(CloudFront 오리진 응답 한계)을 넘겨 "context canceled"로
	// 대시보드 지표 조회 자체가 반복 실패했다(대시보드가 멈춘 것처럼 보였지만
	// 실제 주문 처리/취소는 정상 진행 중이었음 — DB 직접 조회로 확인).
	//
	// 고친 방법: OR가 아니라 buy_order_id/sell_order_id 각각에 대한 별도
	// INNER JOIN 조건(둘 다 trade_order.order_id PK와의 단순 동등 비교)으로
	// 바꿔 왕복을 하나로 합쳤다 — OR-JOIN과 달리 이건 각 JOIN이 독립적인 PK
	// 룩업이라 인덱스를 정상적으로 탄다(2026-08-20 사고의 원인이었던 "OR라서
	// 인덱스를 못 탄다"는 이 구조엔 해당하지 않음). 한쪽 주문이 아직
	// trade_order에 안 들어와 있으면(독립 리더 레이스, recorder/store/
	// apply.go의 ResolveMode가 다루는 것과 같은 상황) INNER JOIN이 그 행을
	// 자연스럽게 제외한다 — 예전의 수동 skip과 동일한 효과.
	var execRows *sql.Rows
	var err error
	const e2eJoinSelect = `
		SELECT e.executed_at, ob.submitted_at, os.submitted_at
		FROM execution e
		JOIN trade_order ob ON ob.order_id = e.buy_order_id
		JOIN trade_order os ON os.order_id = e.sell_order_id
	`
	if window != nil {
		execRows, err = q.db.QueryContext(ctx, e2eJoinSelect+`WHERE e.executed_at >= ? AND e.executed_at < ?`,
			window.From, window.To)
	} else {
		// DB 서버 시각 기준(UTC_TIMESTAMP())으로 계산 — 앱 파드 시계와의 편차
		// 위험을 피하려고 예전과 마찬가지로 SQL 쪽에서 "지금"을 구한다.
		execRows, err = q.db.QueryContext(ctx, e2eJoinSelect+`WHERE e.executed_at >= UTC_TIMESTAMP() - INTERVAL ? MINUTE`,
			int(p99Window.Minutes()))
	}
	if err != nil {
		return DashboardMetrics{}, fmt.Errorf("E2E 지연시간 대상 체결 조회 실패: %w", err)
	}
	var latenciesMs []float64
	for execRows.Next() {
		var executedAt, buySub, sellSub time.Time
		if err := execRows.Scan(&executedAt, &buySub, &sellSub); err != nil {
			execRows.Close()
			return DashboardMetrics{}, fmt.Errorf("E2E 지연시간 대상 체결 조회 실패: %w", err)
		}
		taker := buySub
		if sellSub.After(taker) {
			taker = sellSub
		}
		latenciesMs = append(latenciesMs, float64(executedAt.Sub(taker).Microseconds())/1000.0)
	}
	if err := execRows.Err(); err != nil {
		execRows.Close()
		return DashboardMetrics{}, fmt.Errorf("E2E 지연시간 대상 체결 조회 실패: %w", err)
	}
	execRows.Close()

	m.E2EP99Ms = percentile99(latenciesMs)
	m.E2EP99SampleCount = len(latenciesMs)

	engines, err := q.ListEngines(ctx)
	if err != nil {
		return DashboardMetrics{}, fmt.Errorf("엔진 수 조회 실패: %w", err)
	}
	m.RunningEnginePods = len(engines)

	series, err := q.metricsSeries(ctx)
	if err != nil {
		return DashboardMetrics{}, err
	}
	m.Series = series

	return m, nil
}

// metricsSeries는 최근 seriesWindow(10분)를 seriesBucket(1분) 단위로 잘라
// 접수/체결 건수를 반환합니다. 데이터가 없는 분도 0건으로 명시적으로
// 채웁니다 — 프론트 라인 차트가 빈 구간을 "데이터 없음"과 "0건"으로 헷갈리지
// 않게 하려는 것입니다.
func (q *MySQLQuerier) metricsSeries(ctx context.Context) ([]MetricsBucket, error) {
	orderCounts, err := bucketCounts(ctx, q.db, "trade_order", "submitted_at")
	if err != nil {
		return nil, fmt.Errorf("접수 시계열 조회 실패: %w", err)
	}
	execCounts, err := bucketCounts(ctx, q.db, "execution", "executed_at")
	if err != nil {
		return nil, fmt.Errorf("체결 시계열 조회 실패: %w", err)
	}

	now := time.Now().UTC().Truncate(seriesBucket)
	buckets := int(seriesWindow / seriesBucket)
	out := make([]MetricsBucket, 0, buckets)
	for i := buckets - 1; i >= 0; i-- {
		bucketStart := now.Add(-time.Duration(i) * seriesBucket)
		key := bucketStart.Format(seriesBucketFmt)
		out = append(out, MetricsBucket{
			BucketStart: key,
			Orders:      orderCounts[key],
			Executions:  execCounts[key],
		})
	}
	return out, nil
}

// pickBucketSeconds는 Grafana의 "적당한 step 자동 선택"과 같은 목적입니다 —
// 구간이 넓어질수록 버킷도 넓혀서, 응답 포인트 수를 대략 수십~백 개 선으로
// 유지합니다(1분 버킷으로 24시간을 그리면 1440포인트라 차트도 못 읽고 SQL
// GROUP BY 카디널리티도 쓸데없이 커집니다).
func pickBucketSeconds(span time.Duration) int {
	switch {
	case span <= 30*time.Minute:
		return 60
	case span <= 3*time.Hour:
		return 5 * 60
	case span <= 12*time.Hour:
		return 15 * 60
	default:
		return 30 * 60
	}
}

// ThroughputSeries는 GET /v1/metrics/throughput(2026-08-24)이 씁니다 —
// metricsSeries(고정 10분/1분)와 달리 임의의 [from, to) 구간을 받아 버킷
// 크기를 자동으로 고릅니다. 구간 길이 검증(ThroughputMaxRange)은 이미
// server.go의 throughputHandler가 호출 전에 하지만, 이 메서드를 다른
// 경로에서 직접 불러도 안전하도록 여기서도 한 번 더 확인합니다.
func (q *MySQLQuerier) ThroughputSeries(ctx context.Context, from, to time.Time) ([]MetricsBucket, error) {
	span := to.Sub(from)
	if span <= 0 {
		return nil, fmt.Errorf("from은 to보다 이전이어야 합니다")
	}
	if span > ThroughputMaxRange {
		return nil, fmt.Errorf("구간이 너무 깁니다(최대 %s)", ThroughputMaxRange)
	}
	bucketSeconds := pickBucketSeconds(span)

	orderCounts, err := bucketCountsRange(ctx, q.db, "trade_order", "submitted_at", from, to, bucketSeconds)
	if err != nil {
		return nil, fmt.Errorf("접수 시계열 조회 실패: %w", err)
	}
	execCounts, err := bucketCountsRange(ctx, q.db, "execution", "executed_at", from, to, bucketSeconds)
	if err != nil {
		return nil, fmt.Errorf("체결 시계열 조회 실패: %w", err)
	}

	fromUnix := from.Truncate(time.Duration(bucketSeconds) * time.Second).Unix()
	toUnix := to.Unix()
	out := []MetricsBucket{}
	for t := fromUnix; t < toUnix; t += int64(bucketSeconds) {
		out = append(out, MetricsBucket{
			BucketStart: time.Unix(t, 0).UTC().Format(seriesBucketFmt),
			Orders:      orderCounts[t],
			Executions:  execCounts[t],
		})
	}
	return out, nil
}

// bucketCountsRange는 bucketCounts의 임의 구간/버킷 크기 버전입니다 —
// DATE_FORMAT 대신 초 단위 정수 버킷 키(UNIX_TIMESTAMP를 bucketSeconds로
// 나눠 내림)를 써서, 버킷 크기가 매번 달라져도 같은 쿼리 모양으로 처리합니다.
// CAST(... AS UNSIGNED)가 필요합니다 — FLOOR(UNIX_TIMESTAMP(x)/?)*?는 나눗셈
// 때문에 MySQL이 DECIMAL로 반환하고("1787535600.000...", go-sql-driver가
// []byte로 줌), int64로 바로 Scan하면 실패합니다(2026-08-24 실측).
func bucketCountsRange(ctx context.Context, db *sql.DB, table, tsColumn string, from, to time.Time, bucketSeconds int) (map[int64]int64, error) {
	query := fmt.Sprintf(`
		SELECT CAST(FLOOR(UNIX_TIMESTAMP(%s) / ?) * ? AS UNSIGNED) AS bucket_unix, COUNT(*)
		FROM %s
		WHERE %s >= ? AND %s < ?
		GROUP BY bucket_unix
	`, tsColumn, table, tsColumn, tsColumn)
	rows, err := db.QueryContext(ctx, query, bucketSeconds, bucketSeconds, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[int64]int64)
	for rows.Next() {
		var bucketUnix, count int64
		if err := rows.Scan(&bucketUnix, &count); err != nil {
			return nil, err
		}
		out[bucketUnix] = count
	}
	return out, rows.Err()
}

func bucketCounts(ctx context.Context, db *sql.DB, table, tsColumn string) (map[string]int64, error) {
	// DATE_FORMAT으로 분 단위까지 잘라 SQL에서 바로 그룹핑한다 — Go 쪽에서
	// 시각을 다시 파싱/버킷팅할 필요가 없다.
	query := fmt.Sprintf(`
		SELECT DATE_FORMAT(%s, '%%Y-%%m-%%dT%%H:%%i:00Z') AS bucket, COUNT(*)
		FROM %s
		WHERE %s >= UTC_TIMESTAMP() - INTERVAL ? MINUTE
		GROUP BY bucket
	`, tsColumn, table, tsColumn)
	rows, err := db.QueryContext(ctx, query, int(seriesWindow.Minutes()))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[string]int64)
	for rows.Next() {
		var bucket string
		var count int64
		if err := rows.Scan(&bucket, &count); err != nil {
			return nil, err
		}
		out[bucket] = count
	}
	return out, rows.Err()
}

// percentile99는 정렬되지 않은 밀리초 지연시간 표본에서 P99를 계산합니다.
// 표본이 없으면 0을 반환합니다(호출부가 E2EP99SampleCount로 표본 수를 같이
// 내려주므로, 프론트는 표본이 너무 적을 때 이 값을 무시할 수 있습니다).
func percentile99(samplesMs []float64) float64 {
	if len(samplesMs) == 0 {
		return 0
	}
	sorted := make([]float64, len(samplesMs))
	copy(sorted, samplesMs)
	sort.Float64s(sorted)

	idx := int(0.99*float64(len(sorted))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

// OrderSummary는 mode+[from,to) 구간의 trade_order를 상태별로 집계합니다.
// COALESCE로 SUM을 감싼 이유: 그 구간에 해당하는 행이 하나도 없으면 MySQL의
// SUM(CASE...)이 NULL을 반환하는데(COUNT(*)와 달리), 이걸 그대로 *int64로
// Scan하면 실패합니다 — 0건일 때도 정상적으로 0을 받기 위함입니다.
func (q *MySQLQuerier) OrderSummary(ctx context.Context, mode string, from, to time.Time) (OrderSummary, error) {
	if to.IsZero() {
		to = time.Now().UTC()
	}

	var s OrderSummary
	err := q.db.QueryRowContext(ctx, `
		SELECT
			COUNT(*),
			COALESCE(SUM(CASE WHEN status = 'FILLED' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN status IN ('ACCEPTED', 'PARTIALLY_FILLED') THEN 1 ELSE 0 END), 0)
		FROM trade_order
		WHERE mode = ? AND submitted_at >= ? AND submitted_at < ?
	`, mode, from, to).Scan(&s.Accepted, &s.Filled, &s.Unfilled)
	if err != nil {
		return OrderSummary{}, fmt.Errorf("주문 집계 조회 실패: %w", err)
	}

	byMarket, err := q.orderSummaryByMarket(ctx, mode, from, to)
	if err != nil {
		return OrderSummary{}, err
	}
	s.ByMarket = byMarket

	bySide, err := q.orderSummaryBySide(ctx, mode, from, to)
	if err != nil {
		return OrderSummary{}, err
	}
	s.BySide = bySide

	return s, nil
}

// orderSummaryByMarket은 마켓별 accepted/filled/unfilled를 집계합니다. 위
// 메인 집계와 달리 COALESCE가 필요 없다 — GROUP BY는 결과에 없는 마켓을
// 아예 안 돌려주므로(빈 그룹이 없음), 한 그룹에 행이 1개라도 있으면
// SUM(CASE...)이 NULL이 아니라 항상 0 이상의 숫자가 나온다.
//
// **`FORCE INDEX (idx_trade_order_mode_submitted)` — 2026-08-24 실측 성능
// 버그 수정.** 랙도 0이고 DB도 한가한 상태에서 이 API가 45초씩 걸리는 걸
// 발견해 EXPLAIN으로 확인한 결과, 옵티마이저가 `GROUP BY market_code`의
// 정렬(파일소트)을 피하려고 `idx_trade_order_market_status(market_code, status)`를
// 골랐는데, 이 인덱스엔 mode/submitted_at이 없어서 그 조건을 인덱스로 거르지
// 못하고 **테이블 전체(약 547,157행)를 인덱스 풀스캔**한 뒤에야 걸러냈다 —
// 실제 조건에 맞는 행은 151,733건뿐이라 3.6배를 더 훑은 셈. `mode`+
// `submitted_at`로 먼저 걸러 151K건만 남기고(선택도가 훨씬 좋음), 그
// 결과만 정렬/그룹핑하는 게 훨씬 빠르므로 인덱스를 강제 지정한다 —
// `orderSummaryBySide`도 같은 구조라 동일하게 적용.
func (q *MySQLQuerier) orderSummaryByMarket(ctx context.Context, mode string, from, to time.Time) ([]MarketOrderSummary, error) {
	rows, err := q.db.QueryContext(ctx, `
		SELECT
			market_code,
			COUNT(*),
			SUM(CASE WHEN status = 'FILLED' THEN 1 ELSE 0 END),
			SUM(CASE WHEN status IN ('ACCEPTED', 'PARTIALLY_FILLED') THEN 1 ELSE 0 END)
		FROM trade_order FORCE INDEX (idx_trade_order_mode_submitted)
		WHERE mode = ? AND submitted_at >= ? AND submitted_at < ?
		GROUP BY market_code
		ORDER BY market_code
	`, mode, from, to)
	if err != nil {
		return nil, fmt.Errorf("마켓별 주문 집계 조회 실패: %w", err)
	}
	defer rows.Close()

	out := []MarketOrderSummary{}
	for rows.Next() {
		var m MarketOrderSummary
		if err := rows.Scan(&m.Market, &m.Accepted, &m.Filled, &m.Unfilled); err != nil {
			return nil, fmt.Errorf("마켓별 주문 집계 조회 실패: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// orderSummaryBySide는 매수/매도 접수 건수를 집계합니다. FORCE INDEX 이유는
// orderSummaryByMarket 주석 참고 — 같은 잘못된 인덱스 선택 문제.
func (q *MySQLQuerier) orderSummaryBySide(ctx context.Context, mode string, from, to time.Time) ([]SideOrderSummary, error) {
	rows, err := q.db.QueryContext(ctx, `
		SELECT side, COUNT(*)
		FROM trade_order FORCE INDEX (idx_trade_order_mode_submitted)
		WHERE mode = ? AND submitted_at >= ? AND submitted_at < ?
		GROUP BY side
		ORDER BY side
	`, mode, from, to)
	if err != nil {
		return nil, fmt.Errorf("매수/매도 집계 조회 실패: %w", err)
	}
	defer rows.Close()

	out := []SideOrderSummary{}
	for rows.Next() {
		var sd SideOrderSummary
		if err := rows.Scan(&sd.Side, &sd.Count); err != nil {
			return nil, fmt.Errorf("매수/매도 집계 조회 실패: %w", err)
		}
		out = append(out, sd)
	}
	return out, rows.Err()
}

// IntegrityCheck는 mode+[from,to) 구간의 데이터 정합성을 검사합니다 — 타입
// 설명(query.go의 IntegrityCheck) 참고. 세 쿼리 다 저빈도(대시보드 새로고침
// 시에만) 호출을 전제로 하므로, 다른 쿼리들처럼 인덱스 커버리지에 예민하게
// 최적화하지 않았습니다.
func (q *MySQLQuerier) IntegrityCheck(ctx context.Context, mode string, from, to time.Time) (IntegrityCheck, error) {
	if to.IsZero() {
		to = time.Now().UTC()
	}

	var c IntegrityCheck

	// 중복 체결: 한 주문(매수 또는 매도)에 대해 체결된 수량 합이 원래 주문
	// 수량을 넘는 경우.
	if err := q.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM (
			SELECT f.order_id FROM (
				SELECT buy_order_id AS order_id, quantity FROM execution
				WHERE mode = ? AND executed_at >= ? AND executed_at < ?
				UNION ALL
				SELECT sell_order_id AS order_id, quantity FROM execution
				WHERE mode = ? AND executed_at >= ? AND executed_at < ?
			) f
			JOIN trade_order o ON o.order_id = f.order_id
			GROUP BY f.order_id
			-- MySQL은 HAVING에서 GROUP BY 대상이 아닌 조인 테이블 컬럼(o.quantity)을
			-- 집계 없이 직접 참조하면 Error 1054 "Unknown column"을 냅니다(실측,
			-- 2026-08-24) — o.order_id=f.order_id로 그룹 안에서 항상 같은 값이라
			-- 실제로는 안전한데도 MySQL이 그 함수적 종속성을 HAVING에서는 안
			-- 봐줍니다. MAX()로 감싸면(그룹 내 값이 전부 같으니 결과는 동일) 문법
			-- 요건을 만족시키면서 의미는 그대로입니다.
			HAVING SUM(f.quantity) > MAX(o.quantity)
		) over_filled
	`, mode, from, to, mode, from, to).Scan(&c.DuplicateExecutions); err != nil {
		return IntegrityCheck{}, fmt.Errorf("중복 체결 검사 실패: %w", err)
	}

	// 순서 역전: 체결 시각이 그 체결에 관련된 주문(매수 또는 매도)의 접수
	// 시각보다 이른 경우. LEFT JOIN이라 관련 주문을 못 찾은 쪽은 NULL이 되고,
	// NULL과의 비교는 항상 FALSE라 그 execution은 이 조건으로는 안 걸립니다
	// (주문 유실은 별개 지표에서 다룸).
	if err := q.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM execution e
		LEFT JOIN trade_order ob ON ob.order_id = e.buy_order_id
		LEFT JOIN trade_order os ON os.order_id = e.sell_order_id
		WHERE e.mode = ? AND e.executed_at >= ? AND e.executed_at < ?
			AND (e.executed_at < ob.submitted_at OR e.executed_at < os.submitted_at)
	`, mode, from, to).Scan(&c.SequenceReversals); err != nil {
		return IntegrityCheck{}, fmt.Errorf("순서 역전 검사 실패: %w", err)
	}

	// 매수·매도 총량: 각 주문의 quantity-remaining_quantity(=실제 체결량) 합.
	if err := q.db.QueryRowContext(ctx, `
		SELECT
			COALESCE(SUM(CASE WHEN side = 'BUY'  THEN quantity - remaining_quantity ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN side = 'SELL' THEN quantity - remaining_quantity ELSE 0 END), 0)
		FROM trade_order
		WHERE mode = ? AND submitted_at >= ? AND submitted_at < ?
	`, mode, from, to).Scan(&c.BuyFilled, &c.SellFilled); err != nil {
		return IntegrityCheck{}, fmt.Errorf("매수매도 총량 검사 실패: %w", err)
	}

	return c, nil
}

// UnresolvedOrders는 mode+[from,to) 구간에서 아직 ACCEPTED/PARTIALLY_FILLED
// 상태인 주문의 ID+마켓을 전부 반환합니다. to가 zero value면 OrderSummary와
// 같은 이유로 지금 시각을 씁니다.
func (q *MySQLQuerier) UnresolvedOrders(ctx context.Context, mode string, from, to time.Time) ([]UnresolvedOrder, error) {
	if to.IsZero() {
		to = time.Now().UTC()
	}

	rows, err := q.db.QueryContext(ctx, `
		SELECT order_id, market_code
		FROM trade_order
		WHERE mode = ? AND submitted_at >= ? AND submitted_at < ?
			AND status IN ('ACCEPTED', 'PARTIALLY_FILLED')
	`, mode, from, to)
	if err != nil {
		return nil, fmt.Errorf("미종결 주문 조회 실패: %w", err)
	}
	defer rows.Close()

	orders := []UnresolvedOrder{}
	for rows.Next() {
		var o UnresolvedOrder
		if err := rows.Scan(&o.OrderID, &o.Market); err != nil {
			return nil, fmt.Errorf("미종결 주문 스캔 실패: %w", err)
		}
		orders = append(orders, o)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("미종결 주문 조회 실패: %w", err)
	}
	return orders, nil
}

// AllUnresolvedOrders는 mode/기간 제한 없이 지금 ACCEPTED/PARTIALLY_FILLED
// 상태인 주문 전부의 ID+마켓을 반환합니다.
func (q *MySQLQuerier) AllUnresolvedOrders(ctx context.Context) ([]UnresolvedOrder, error) {
	rows, err := q.db.QueryContext(ctx, `
		SELECT order_id, market_code
		FROM trade_order
		WHERE status IN ('ACCEPTED', 'PARTIALLY_FILLED')
	`)
	if err != nil {
		return nil, fmt.Errorf("전체 미종결 주문 조회 실패: %w", err)
	}
	defer rows.Close()

	orders := []UnresolvedOrder{}
	for rows.Next() {
		var o UnresolvedOrder
		if err := rows.Scan(&o.OrderID, &o.Market); err != nil {
			return nil, fmt.Errorf("전체 미종결 주문 스캔 실패: %w", err)
		}
		orders = append(orders, o)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("전체 미종결 주문 조회 실패: %w", err)
	}
	return orders, nil
}
