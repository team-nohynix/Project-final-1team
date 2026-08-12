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
const (
	tpsWindow       = 60 * time.Second
	p99Window       = 5 * time.Minute
	seriesWindow    = 10 * time.Minute
	seriesBucket    = time.Minute
	seriesBucketFmt = "2006-01-02T15:04:00Z" // 분 단위로 잘라 표시(초는 항상 00)
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
	OrderAcceptTps    float64         `json:"orderAcceptTps"`
	ExecutionTps      float64         `json:"executionTps"`
	PendingOrders     int64           `json:"pendingOrders"`
	E2EP99Ms          float64         `json:"e2eP99Ms"`
	E2EP99SampleCount int             `json:"e2eP99SampleCount"`
	RunningEnginePods int             `json:"runningEnginePods"`
	Series            []MetricsBucket `json:"series"`
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

	// E2E P99: 최근 p99Window 안에 FILLED된 주문마다 (마지막 체결 시각 - 접수
	// 시각)을 표본 하나로 삼는다. DashboardMetrics 타입 주석 참고 — 완벽한
	// "시스템 처리 지연"은 아니지만 현재 스키마로 낼 수 있는 최선의 근사치.
	rows, err := q.db.QueryContext(ctx, `
		SELECT TIMESTAMPDIFF(MICROSECOND, o.submitted_at, MAX(e.executed_at))
		FROM trade_order o
		JOIN execution e ON (e.buy_order_id = o.order_id OR e.sell_order_id = o.order_id)
		WHERE o.status = 'FILLED' AND o.submitted_at >= UTC_TIMESTAMP() - INTERVAL ? MINUTE
		GROUP BY o.order_id
	`, int(p99Window.Minutes()))
	if err != nil {
		return DashboardMetrics{}, fmt.Errorf("E2E 지연시간 조회 실패: %w", err)
	}
	var latenciesMs []float64
	for rows.Next() {
		var microseconds int64
		if err := rows.Scan(&microseconds); err != nil {
			rows.Close()
			return DashboardMetrics{}, fmt.Errorf("E2E 지연시간 조회 실패: %w", err)
		}
		latenciesMs = append(latenciesMs, float64(microseconds)/1000.0)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return DashboardMetrics{}, fmt.Errorf("E2E 지연시간 조회 실패: %w", err)
	}
	rows.Close()
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
