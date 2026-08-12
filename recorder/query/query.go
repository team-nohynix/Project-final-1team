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

	"recorder/store"
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
