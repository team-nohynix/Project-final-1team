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
	"strings"
	"time"

	"recorder/store"
)

// tpsWindow/p99Window/seriesWindow/seriesBucket은 DashboardMetrics의 계산
// 창입니다 — 실측 후 조정할 잠정값입니다(backpressure 워터마크와 같은 성격).
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
	// 쌓아둔 데이터에서 계산해 반환합니다.
	DashboardMetrics(ctx context.Context) (DashboardMetrics, error)
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
// PARTIALLY_FILLED)의 스냅샷입니다. E2EP99Ms는 "주문 접수 시각 →
// 그 주문을 완전 체결시킨 마지막 executed_at" 구간을 최근
// p99WindowMinutes(5분) 안에 FILLED된 주문들에서 표본으로 삼아 계산합니다 —
// 매수/매도 중 나중에 들어와 체결을 촉발한 쪽(공격적 주문)에게는 실제
// "시스템 처리 지연"에 가깝지만, 오래 대기하다 체결된 지정가(수동적) 주문
// 쪽에서 보면 "시장에서 기다린 시간"까지 섞여 들어갈 수 있습니다 — 현재
// 스키마엔 "매칭 시작/큐 대기" 같은 중간 단계 타임스탬프가 없어 더 정밀하게
// 나눌 방법이 없습니다(recorder/server.go traceHandler 주석의 5단계 파이프라인
// 부재와 같은 근본 원인). RunningEnginePods는 ListEngines 결과의 distinct
// engineInstanceId 수를 그대로 씁니다.
type DashboardMetrics struct {
	OrderAcceptTps    float64          `json:"orderAcceptTps"`
	ExecutionTps      float64          `json:"executionTps"`
	PendingOrders     int64            `json:"pendingOrders"`
	E2EP99Ms          float64          `json:"e2eP99Ms"`
	E2EP99SampleCount int              `json:"e2eP99SampleCount"`
	RunningEnginePods int              `json:"runningEnginePods"`
	Series            []MetricsBucket  `json:"series"`
	OrdersByStatus    map[string]int64 `json:"ordersByStatus"`
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

func (q *MySQLQuerier) DashboardMetrics(ctx context.Context) (DashboardMetrics, error) {
	var m DashboardMetrics

	var acceptCount, execCount, pendingCount int64
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
	if err := q.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM trade_order WHERE status IN ('ACCEPTED', 'PARTIALLY_FILLED')`,
	).Scan(&pendingCount); err != nil {
		return DashboardMetrics{}, fmt.Errorf("처리 대기 주문 조회 실패: %w", err)
	}
	m.OrderAcceptTps = float64(acceptCount) / tpsWindow.Seconds()
	m.ExecutionTps = float64(execCount) / tpsWindow.Seconds()
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

	// E2E P99: 최근 p99Window 안에 FILLED된 주문마다 (마지막 체결 시각 - 접수
	// 시각)을 표본 하나로 삼는다. DashboardMetrics 타입 주석 참고 — 완벽한
	// "시스템 처리 지연"은 아니지만 현재 스키마로 낼 수 있는 최선의 근사치.
	//
	// 원래는 `trade_order o JOIN execution e ON (e.buy_order_id = o.order_id
	// OR e.sell_order_id = o.order_id)` 한 방 쿼리였다 — OR가 execution의 두
	// 컬럼에 걸쳐 있어 옵티마이저가 idx_execution_buy_order/
	// idx_execution_sell_order 어느 쪽도 못 타고 execution 전체(500만행+)를
	// 매 폴링(10초)마다 스캔했다(2026-08-20, EXPLAIN으로 rows:5,063,699 확인
	// — pollDashboardMetrics가 DB CPU를 계속 붙잡고 있던 원인). 대신 ①먼저
	// idx_trade_order_status_submitted로 최근 창의 FILLED 주문만 좁게 골라낸
	// 뒤 ②execution을 buy_order_id/sell_order_id 각각 자기 인덱스로 따로
	// 조회해서 Go에서 합친다 — OR-JOIN 자체를 없애 최근 창 크기에만 비례하게
	// 만든다(schema.sql의 idx_trade_order_status_submitted 주석 참고).
	orderRows, err := q.db.QueryContext(ctx, `
		SELECT order_id, submitted_at FROM trade_order
		WHERE status = 'FILLED' AND submitted_at >= UTC_TIMESTAMP() - INTERVAL ? MINUTE
	`, int(p99Window.Minutes()))
	if err != nil {
		return DashboardMetrics{}, fmt.Errorf("E2E 지연시간 대상 주문 조회 실패: %w", err)
	}
	submittedAt := make(map[string]time.Time)
	var orderIDs []string
	for orderRows.Next() {
		var id string
		var t time.Time
		if err := orderRows.Scan(&id, &t); err != nil {
			orderRows.Close()
			return DashboardMetrics{}, fmt.Errorf("E2E 지연시간 대상 주문 조회 실패: %w", err)
		}
		submittedAt[id] = t
		orderIDs = append(orderIDs, id)
	}
	if err := orderRows.Err(); err != nil {
		orderRows.Close()
		return DashboardMetrics{}, fmt.Errorf("E2E 지연시간 대상 주문 조회 실패: %w", err)
	}
	orderRows.Close()

	maxExecutedAt := make(map[string]time.Time, len(orderIDs))
	if len(orderIDs) > 0 {
		if err := q.mergeMaxExecutedAt(ctx, "buy_order_id", orderIDs, maxExecutedAt); err != nil {
			return DashboardMetrics{}, err
		}
		if err := q.mergeMaxExecutedAt(ctx, "sell_order_id", orderIDs, maxExecutedAt); err != nil {
			return DashboardMetrics{}, err
		}
	}

	var latenciesMs []float64
	for _, id := range orderIDs {
		executedAt, ok := maxExecutedAt[id]
		if !ok {
			continue
		}
		latenciesMs = append(latenciesMs, float64(executedAt.Sub(submittedAt[id]).Microseconds())/1000.0)
	}
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

// mergeMaxExecutedAt은 column(buy_order_id 또는 sell_order_id)이 orderIDs에
// 속하는 execution 행들의 executed_at을 조회해 out에 주문별 최댓값으로
// 병합합니다. buy_order_id/sell_order_id를 OR 하나로 묶지 않고 이렇게 따로
// 호출해야 각자의 단일 컬럼 인덱스(idx_execution_buy_order/
// idx_execution_sell_order)를 탈 수 있습니다 — DashboardMetrics 주석 참고.
// column은 이 파일 안에서 "buy_order_id"/"sell_order_id" 리터럴로만 호출되므로
// SQL 인젝션 경로가 아닙니다.
func (q *MySQLQuerier) mergeMaxExecutedAt(ctx context.Context, column string, orderIDs []string, out map[string]time.Time) error {
	placeholders := strings.Repeat("?,", len(orderIDs))
	placeholders = placeholders[:len(placeholders)-1]
	args := make([]any, len(orderIDs))
	for i, id := range orderIDs {
		args[i] = id
	}
	rows, err := q.db.QueryContext(ctx, fmt.Sprintf(
		`SELECT %s, executed_at FROM execution WHERE %s IN (%s)`, column, column, placeholders,
	), args...)
	if err != nil {
		return fmt.Errorf("E2E 지연시간(%s) 조회 실패: %w", column, err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var t time.Time
		if err := rows.Scan(&id, &t); err != nil {
			return fmt.Errorf("E2E 지연시간(%s) 조회 실패: %w", column, err)
		}
		if cur, ok := out[id]; !ok || t.After(cur) {
			out[id] = t
		}
	}
	return rows.Err()
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
func (q *MySQLQuerier) orderSummaryByMarket(ctx context.Context, mode string, from, to time.Time) ([]MarketOrderSummary, error) {
	rows, err := q.db.QueryContext(ctx, `
		SELECT
			market_code,
			COUNT(*),
			SUM(CASE WHEN status = 'FILLED' THEN 1 ELSE 0 END),
			SUM(CASE WHEN status IN ('ACCEPTED', 'PARTIALLY_FILLED') THEN 1 ELSE 0 END)
		FROM trade_order
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

// orderSummaryBySide는 매수/매도 접수 건수를 집계합니다.
func (q *MySQLQuerier) orderSummaryBySide(ctx context.Context, mode string, from, to time.Time) ([]SideOrderSummary, error) {
	rows, err := q.db.QueryContext(ctx, `
		SELECT side, COUNT(*)
		FROM trade_order
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
