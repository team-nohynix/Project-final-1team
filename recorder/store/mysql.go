package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"recorder/idgen"
)

// isoLayout은 이 repo 전체가 쓰는 ISO-8601 UTC ms-정밀도 시각 형식입니다
// (orderapi/server.go의 nowISO(), matching/kafkaclient/assignment_producer.go).
const isoLayout = "2006-01-02T15:04:05.000Z"

// parseTimestamp는 위 형식의 문자열을 time.Time으로 바꿉니다 — go-sql-driver/mysql은
// DATETIME 컬럼에 문자열을 그대로 바인딩하면 안 됩니다(MySQL 자체 DATETIME
// 리터럴 문법은 'T'/'Z' 없이 공백으로 구분 — ISO-8601 문자열을 곧이곧대로
// 넘기면 "Incorrect datetime value" 에러가 납니다, 실제 검증 중 발견). time.Time
// 값으로 바인딩하면 드라이버가 올바르게 변환해줍니다.
func parseTimestamp(s string) (time.Time, error) {
	t, err := time.Parse(isoLayout, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("시각 파싱 실패 (%q): %w", s, err)
	}
	return t, nil
}

// MySQLStore는 Store를 실제 MySQL(RDS)로 구현합니다. 이 파일은 손으로
// 검증합니다(matching/kafkaclient·snapshotstore와 같은 이유) — 실제 DB 왕복이
// 핵심이라 단위 테스트로는 의미 있게 못 검증합니다.
type MySQLStore struct {
	db *sql.DB
}

func NewMySQLStore(db *sql.DB) *MySQLStore {
	return &MySQLStore{db: db}
}

// InsertOrdersBatch는 신규 주문 여러 건을 한 번의 다중 행 INSERT로 저장합니다
// (RDS 백프레셔 대응 배칭, 2026-08-07 — CLAUDE.md 참고. 이전엔 건당 한 번씩
// ExecContext를 불렀는데, 그걸 여러 건을 한 SQL 문에 묶어 왕복 횟수를 줄인
// 것입니다). remaining_quantity는 최초엔 quantity와 같습니다. 같은 order_id가
// 이미 있는 행은(재전달 등) INSERT IGNORE가 그 행만 조용히 무시합니다 —
// 여러 행을 한 문장에 묶어도 IGNORE는 행 단위로 적용되므로 한 건이 중복이라고
// 나머지 건까지 실패하지 않습니다.
func (s *MySQLStore) InsertOrdersBatch(ctx context.Context, orders []NewOrder) error {
	if len(orders) == 0 {
		return nil
	}

	var sb strings.Builder
	sb.WriteString(`INSERT IGNORE INTO trade_order
		(order_id, client_request_id, market_code, side, price, quantity, remaining_quantity, status, mode, submitted_at, source_order_id)
		VALUES `)
	args := make([]any, 0, len(orders)*10)
	for i, o := range orders {
		if i > 0 {
			sb.WriteString(",")
		}
		sb.WriteString("(?, ?, ?, ?, ?, ?, ?, 'ACCEPTED', ?, ?, ?)")

		submittedAt, err := parseTimestamp(o.SubmittedAt)
		if err != nil {
			return fmt.Errorf("주문 일괄 저장 실패 (orderId=%s): %w", o.OrderID, err)
		}
		args = append(args, o.OrderID, nullIfEmpty(o.ClientRequestID), o.Market, o.Side, o.Price, o.Quantity, o.Quantity, o.Mode, submittedAt, nullIfEmpty(o.SourceOrderID))
	}

	if _, err := s.db.ExecContext(ctx, sb.String(), args...); err != nil {
		return fmt.Errorf("주문 일괄 저장 실패 (%d건): %w", len(orders), err)
	}
	return nil
}

// CancelOrdersBatch는 취소 여러 건을 한 트랜잭션(=한 번의 커밋) 안에서
// 처리합니다. UPDATE 문 자체는 건당 하나씩 실행되지만(각 취소가 서로 다른
// order_id를 대상으로 해서 하나의 다중 행 UPDATE로 합치기 어려움), 트랜잭션을
// 하나로 묶는 것만으로도 커밋 횟수(= RDS에서 상대적으로 비싼 연산)를 건수만큼이
// 아니라 배치당 1번으로 줄일 수 있습니다. 대상 주문이 없는 항목(NEW를 못 본
// CANCEL)은 그 항목만 영향받는 행 0개로 조용히 넘어갑니다 — 에러가 아닙니다.
//
// WHERE에 FILLED도 막는 이유(2026-08-07, 실제 발생 가능한 케이스 분석 후 결정) —
// orders/executions는 기록기가 서로 독립적으로 소비하므로(배칭으로 그 창이 더
// 벌어짐), 어떤 주문이 완전 체결된 *뒤에* 그 주문에 대한 취소가 기록기에 도착할
// 수 있습니다(가장 흔한 경로: orderapi의 order.Store가 체결 결과를 몰라 이미
// 체결된 주문의 취소 요청도 그냥 받아서 발행해버림 — server.go의 "still never
// learns about fills" 참고). 완전 체결은 매칭엔진의 호가창에서 이미 확정된
// 사실이라 뒤늦은 취소가 그걸 덮어쓰면 안 됩니다 — updateFill이 반대 방향
// (체결이 취소를 정정)을 처리하는 것과 대칭되는 보호입니다.
func (s *MySQLStore) CancelOrdersBatch(ctx context.Context, cancels []CancelInput) error {
	if len(cancels) == 0 {
		return nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("취소 일괄 반영 실패: %w", err)
	}
	defer tx.Rollback()

	for _, c := range cancels {
		parsed, err := parseTimestamp(c.CanceledAt)
		if err != nil {
			return fmt.Errorf("취소 일괄 반영 실패 (orderId=%s): %w", c.OrderID, err)
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE trade_order
			SET status = 'CANCELED', canceled_at = ?
			WHERE order_id = ? AND status NOT IN ('CANCELED', 'FILLED')
		`, parsed, c.OrderID); err != nil {
			return fmt.Errorf("취소 일괄 반영 실패 (orderId=%s): %w", c.OrderID, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("취소 일괄 반영 실패 (커밋): %w", err)
	}
	return nil
}

// ApplyExecutionsBatch는 체결 여러 건을 한 트랜잭션(=한 번의 커밋) 안에서:
// 각 체결의 매수/매도 양쪽 주문 remaining_quantity/status를 updateFill로
// 갱신하고(건당 로직은 단건 버전과 동일 — 각 체결이 서로 다른 주문 쌍을
// 건드릴 수 있어 이 부분은 다중 행으로 합치기 어려움), execution 행들은 한
// 번의 다중 행 INSERT로 저장합니다. 트랜잭션 하나로 묶이는 것 자체가 배칭의
// 핵심 이득입니다 — 건당 트랜잭션(건당 커밋)이던 것을 배치당 트랜잭션(배치당
// 커밋 1번)으로 줄여, RDS 쪽 커밋 오버헤드가 건수가 아니라 배치 수에 비례하게
// 만듭니다. 반환값은 execs와 같은 순서·같은 길이입니다.
func (s *MySQLStore) ApplyExecutionsBatch(ctx context.Context, execs []ExecutionInput) ([]ExecutionResult, error) {
	if len(execs) == 0 {
		return nil, nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("체결 일괄 반영 실패: %w", err)
	}
	defer tx.Rollback()

	results := make([]ExecutionResult, len(execs))
	var sb strings.Builder
	sb.WriteString(`INSERT INTO execution (execution_id, market_code, buy_order_id, sell_order_id, price, quantity, mode, executed_at) VALUES `)
	args := make([]any, 0, len(execs)*7)

	for i, in := range execs {
		buyMode, buyFound, err := updateFill(ctx, tx, in.BuyOrderID, in.Quantity)
		if err != nil {
			return nil, err
		}
		sellMode, sellFound, err := updateFill(ctx, tx, in.SellOrderID, in.Quantity)
		if err != nil {
			return nil, err
		}

		mode, mismatched := ResolveMode(buyMode, buyFound, sellMode, sellFound)
		execID := idgen.NewExecutionID()

		if i > 0 {
			sb.WriteString(",")
		}
		sb.WriteString("(?, ?, ?, ?, ?, ?, ?, UTC_TIMESTAMP())")
		args = append(args, execID, in.Market, in.BuyOrderID, in.SellOrderID, in.Price, in.Quantity, nullIfEmpty(mode))

		results[i] = ExecutionResult{
			ExecutionID: execID, Mode: mode,
			BuyFound: buyFound, SellFound: sellFound, ModeMismatched: mismatched,
		}
	}

	if _, err := tx.ExecContext(ctx, sb.String(), args...); err != nil {
		return nil, fmt.Errorf("체결 일괄 저장 실패 (%d건): %w", len(execs), err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("체결 일괄 반영 실패 (커밋): %w", err)
	}

	return results, nil
}

// updateFill은 한 주문의 remaining_quantity를 qty만큼 SQL 안에서 직접 줄이고
// (Go에서 decimal 연산을 다시 구현할 필요 없음) status를 그 결과에 맞게 바꿉니다.
// remaining_quantity는 이 주문이 이미 CANCELED든 아니든 항상 정확히 반영합니다
// — 실제 체결 사실 자체는 도착 순서와 무관하게 항상 진실이므로, 늦게 도착한
// 체결이라도 반영을 건너뛰면 잔량이 영원히 틀린 값으로 남습니다.
//
// status의 CASE 순서가 중요합니다(2026-08-07, 실제 발생 케이스 분석 후 결정) —
// ① "이번 체결로 잔량이 0 이하가 됐다"가 최우선입니다: 이건 그 주문이 실제로
// 완전 체결됐다는 확정적 증거이므로, 이미 CANCELED로 찍혀 있었더라도(취소가
// 이 체결보다 먼저 기록기에 도착한 경우 — orders/executions를 독립적으로
// 소비하다 보니 실제로 일어남) FILLED로 정정합니다. ② 잔량이 아직 남았는데
// 이미 CANCELED면 그대로 유지합니다 — 정상적인 "부분체결 후 취소" 흐름에서
// 늦게 도착한 부분체결이 취소를 되돌리면 안 되기 때문입니다. ③ 그 외엔 기존과
// 같이 PARTIALLY_FILLED. CancelOrdersBatch의 FILLED 보호와 대칭을 이룹니다.
// ①로 FILLED가 확정되는 순간 canceled_at도 함께 NULL로 지웁니다 — 그 전에
// 뒤늦게 잘못 찍혔던 CANCELED 상태의 흔적(취소 시각)을 같이 정리해서, "체결
// 완료됐는데 취소 시각도 남아있는" 모순된 행이 남지 않게 합니다.
func updateFill(ctx context.Context, tx *sql.Tx, orderID, qty string) (mode string, found bool, err error) {
	res, err := tx.ExecContext(ctx, `
		UPDATE trade_order
		SET remaining_quantity = remaining_quantity - ?,
		    status = CASE
		        WHEN remaining_quantity - ? <= 0 THEN 'FILLED'
		        WHEN status = 'CANCELED' THEN 'CANCELED'
		        ELSE 'PARTIALLY_FILLED'
		    END,
		    canceled_at = CASE WHEN remaining_quantity - ? <= 0 THEN NULL ELSE canceled_at END
		WHERE order_id = ?
	`, qty, qty, qty, orderID)
	if err != nil {
		return "", false, fmt.Errorf("주문 체결 반영 실패 (orderId=%s): %w", orderID, err)
	}

	affected, err := res.RowsAffected()
	if err != nil {
		return "", false, fmt.Errorf("영향받은 행 수 확인 실패 (orderId=%s): %w", orderID, err)
	}
	if affected == 0 {
		return "", false, nil
	}

	if err := tx.QueryRowContext(ctx, `SELECT mode FROM trade_order WHERE order_id = ?`, orderID).Scan(&mode); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("주문 mode 조회 실패 (orderId=%s): %w", orderID, err)
	}
	return mode, true, nil
}

// AssignMarket은 FR-11 배정 이벤트를 새 행으로 기록합니다(released_at은 아직 NULL).
// 먼저 이 마켓에 아직 열려 있는(released_at IS NULL) 배정이 있으면 닫습니다 —
// 정상적인 경우 이전 담당자의 Release가 이미 닫아놔서 여기선 0건에 영향을 줄
// 뿐이지만, 이전 담당자가 강제 종료 등으로 RELEASED를 못 보낸 경우(실제 검증
// 중 발견 — 오래된 open 행이 영원히 남아 "released_at IS NULL로 지금 담당자를
// 알 수 있다"는 이 테이블의 존재 목적 자체가 깨졌었음) 새 ASSIGNED가 유일한
// 진짜 담당자라는 걸 알 수 있습니다 — Kafka 그룹의 Generation.Start 계약("새
// 배정을 받았다는 건 이전 담당자가 이미 완전히 멈췄다")이 이 가정을 보장합니다.
func (s *MySQLStore) AssignMarket(ctx context.Context, in AssignmentInput) error {
	assignedAt, err := parseTimestamp(in.At)
	if err != nil {
		return fmt.Errorf("마켓 배정 기록 실패 (market=%s): %w", in.Market, err)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("마켓 배정 기록 실패 (market=%s): %w", in.Market, err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `
		UPDATE matching_engine_assignment
		SET released_at = ?
		WHERE market_code = ? AND released_at IS NULL
	`, assignedAt, in.Market); err != nil {
		return fmt.Errorf("마켓 배정 기록 실패 (이전 배정 정리, market=%s): %w", in.Market, err)
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO matching_engine_assignment (assignment_id, market_code, engine_instance_id, assigned_at)
		VALUES (?, ?, ?, ?)
	`, idgen.NewAssignmentID(), in.Market, in.EngineInstanceID, assignedAt); err != nil {
		return fmt.Errorf("마켓 배정 기록 실패 (market=%s): %w", in.Market, err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("마켓 배정 기록 실패 (market=%s): %w", in.Market, err)
	}
	return nil
}

// ReleaseMarket은 해당 마켓·인스턴스의 열려 있는 배정을 반납 처리합니다. 그런
// 배정이 없으면(ASSIGNED 이벤트를 못 본 경우) 영향받는 행이 0개일 뿐, 에러가
// 아닙니다 — CancelOrder와 같은 이유.
func (s *MySQLStore) ReleaseMarket(ctx context.Context, in AssignmentInput) error {
	releasedAt, err := parseTimestamp(in.At)
	if err != nil {
		return fmt.Errorf("마켓 반납 기록 실패 (market=%s): %w", in.Market, err)
	}

	_, err = s.db.ExecContext(ctx, `
		UPDATE matching_engine_assignment
		SET released_at = ?
		WHERE market_code = ? AND engine_instance_id = ? AND released_at IS NULL
	`, releasedAt, in.Market, in.EngineInstanceID)
	if err != nil {
		return fmt.Errorf("마켓 반납 기록 실패 (market=%s): %w", in.Market, err)
	}
	return nil
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
