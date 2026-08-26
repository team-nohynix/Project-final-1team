package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"

	"recorder/idgen"
)

// ISOLayout은 이 repo 전체가 쓰는 ISO-8601 UTC ms-정밀도 시각 형식입니다
// (orderapi/server.go의 nowISO(), matching/kafkaclient/assignment_producer.go).
// query 패키지도 DB에서 읽은 time.Time을 같은 형식의 문자열로 되돌릴 때 이 값을
// 그대로 씁니다 — recorder 모듈 안에서는 굳이 같은 상수를 또 선언하지 않습니다
// (모듈 간 재선언 원칙은 이 repo의 서로 다른 Go 모듈 사이에만 적용되는 것이고,
// 같은 모듈의 두 패키지가 상수 하나를 공유하는 건 평범한 Go 관례입니다).
const ISOLayout = "2006-01-02T15:04:05.000Z"

// parseTimestamp는 위 형식의 문자열을 time.Time으로 바꿉니다 — go-sql-driver/mysql은
// DATETIME 컬럼에 문자열을 그대로 바인딩하면 안 됩니다(MySQL 자체 DATETIME
// 리터럴 문법은 'T'/'Z' 없이 공백으로 구분 — ISO-8601 문자열을 곧이곧대로
// 넘기면 "Incorrect datetime value" 에러가 납니다, 실제 검증 중 발견). time.Time
// 값으로 바인딩하면 드라이버가 올바르게 변환해줍니다.
func parseTimestamp(s string) (time.Time, error) {
	t, err := time.Parse(ISOLayout, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("시각 파싱 실패 (%q): %w", s, err)
	}
	return t, nil
}

// maxDBRetries/baseDBRetryWait/maxDBRetryWait는 데드락/락 대기 타임아웃
// 재시도 파라미터입니다(2026-08-11 부하 테스트 중 발견 — 아래 설명 참고).
// 이 프로젝트 다른 재시도 파라미터(trader/order/retry.go)들과 같은 이유로
// 실측 없이 잡은 잠정값입니다.
const (
	maxDBRetries    = 5
	baseDBRetryWait = 50 * time.Millisecond
	maxDBRetryWait  = 1 * time.Second
)

// isRetryableMySQLError는 데드락(1213)/락 대기 타임아웃(1205)인지 판단합니다.
// 둘 다 InnoDB에서 동시 쓰기 트랜잭션이 같은 행을 다른 순서로 잠글 때 정상적으로
// 발생할 수 있는 일시적 상황이지 버그가 아닙니다 — 트랜잭션을 처음부터 다시
// 실행하면 대개 성공합니다(MySQL 자체가 에러 메시지에도 "try restarting
// transaction"이라고 안내). 2026-08-11 실측 부하 테스트에서 실제로 발견:
// orders 리더(신규 주문 INSERT)와 executions 리더(체결 반영 UPDATE)가 같은
// order_id를 거의 동시에 서로 다른 순서로 건드리면서 진짜 데드락이 났고,
// 기존엔 이걸 그냥 치명적 에러로 취급해 프로세스를 통째로 재시작했는데(오프셋
// 미커밋 상태라 재시작하면 같은 배치를 다시 시도하는 방식) 지속적인 동시
// 부하 아래서는 재시작해도 같은 충돌 패턴이 또 나서 사실상 멈춰버렸습니다.
// 프로세스 재시작 대신 이 함수 안에서 바로 재시도하면 훨씬 빠르고 덜
// 파괴적입니다.
func isRetryableMySQLError(err error) bool {
	var myErr *mysql.MySQLError
	if !errors.As(err, &myErr) {
		return false
	}
	return myErr.Number == 1213 || myErr.Number == 1205
}

// withRetryOnDeadlock은 fn(보통 BeginTx...Commit 전체, 또는 단일 쓰기 문 하나)을
// 실행하고 데드락/락 대기 타임아웃이면 지수 백오프+지터로 재시도합니다. fn은
// 매번 처음부터 다시 실행 가능해야 합니다 — 실패한 트랜잭션은 재개할 수 없으므로
// 호출부는 항상 fn 안에서 새 트랜잭션을 시작합니다. 지터를 더하는 이유는
// trader/order/retry.go의 RetryingSubmitter와 같습니다 — 데드락은 보통 두
// 트랜잭션이 거의 동시에 충돌해서 나므로, 지터 없이 똑같은 간격으로 재시도하면
// 또 동시에 부딪힐 수 있습니다.
func withRetryOnDeadlock(fn func() error) error {
	var lastErr error
	for attempt := 0; attempt <= maxDBRetries; attempt++ {
		err := fn()
		if err == nil {
			return nil
		}
		if !isRetryableMySQLError(err) {
			return err
		}
		lastErr = err
		if attempt == maxDBRetries {
			break
		}
		wait := baseDBRetryWait * time.Duration(1<<uint(attempt))
		if wait > maxDBRetryWait {
			wait = maxDBRetryWait
		}
		wait += time.Duration(rand.Int63n(int64(wait)/5 + 1))
		time.Sleep(wait)
	}
	return lastErr
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
//
// INSERT 직후 reconcilePreexistingFills를 항상 호출합니다(2026-08-12 실사용
// 검증 중 발견한 버그 수정) — orders/executions를 독립된 배치로 소비하다 보니
// 이 주문에 대한 체결이 이 INSERT보다 먼저 recorder에 도착해 있을 수 있는데,
// 그때 applyFillsBatch는 order_id를 못 찾아(found=false) 그냥 넘어가고, execution
// 행 자체는 저장되지만 trade_order 쪽엔 그 체결의 효과가 영영 반영되지
// 않았습니다. 아래 함수 주석 참고.
func (s *MySQLStore) InsertOrdersBatch(ctx context.Context, orders []NewOrder) (int64, error) {
	if len(orders) == 0 {
		return 0, nil
	}

	var sb strings.Builder
	sb.WriteString(`INSERT IGNORE INTO trade_order
		(order_id, client_request_id, market_code, side, price, quantity, remaining_quantity, status, mode, submitted_at, source_order_id)
		VALUES `)
	args := make([]any, 0, len(orders)*10)
	orderIDs := make([]string, len(orders))
	for i, o := range orders {
		if i > 0 {
			sb.WriteString(",")
		}
		sb.WriteString("(?, ?, ?, ?, ?, ?, ?, 'ACCEPTED', ?, ?, ?)")

		submittedAt, err := parseTimestamp(o.SubmittedAt)
		if err != nil {
			return 0, fmt.Errorf("주문 일괄 저장 실패 (orderId=%s): %w", o.OrderID, err)
		}
		args = append(args, o.OrderID, nullIfEmpty(o.ClientRequestID), o.Market, o.Side, o.Price, o.Quantity, o.Quantity, o.Mode, submittedAt, nullIfEmpty(o.SourceOrderID))
		orderIDs[i] = o.OrderID
	}

	var inserted int64
	err := withRetryOnDeadlock(func() error {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("주문 일괄 저장 실패: %w", err)
		}
		defer tx.Rollback()

		res, err := tx.ExecContext(ctx, sb.String(), args...)
		if err != nil {
			return fmt.Errorf("주문 일괄 저장 실패 (%d건): %w", len(orders), err)
		}
		// INSERT IGNORE는 재전달로 스킵된 중복 행을 RowsAffected에서 제외합니다 —
		// 이 값을 그대로 접수 카운터(recorder/metrics.go)에 쓰면 중복 재전달이
		// TPS를 부풀리지 않습니다. 재시도(데드락 등)가 나면 이 클로저가 통째로
		// 다시 실행되므로, 최종적으로 err==nil로 반환될 때의 값만 유효합니다.
		inserted, err = res.RowsAffected()
		if err != nil {
			return fmt.Errorf("영향받은 행 수 확인 실패: %w", err)
		}
		if err := reconcilePreexistingFills(ctx, tx, orderIDs); err != nil {
			return fmt.Errorf("선도착 체결 반영 실패: %w", err)
		}

		if err := tx.Commit(); err != nil {
			return fmt.Errorf("주문 일괄 저장 실패 (커밋): %w", err)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return inserted, nil
}

// reconcilePreexistingFills는 이 배치의 order_id들에 대해 execution 테이블에
// 이미 쌓여 있는 체결이 있으면 그 총량을 trade_order에 반영합니다.
//
// 왜 이게 안전한가(멱등) — execution 테이블은 applyFillsBatch가 order_id를
// 찾았는지와 무관하게 항상 그 행을 저장합니다(ApplyExecutionsBatch,
// FR-09 검증 기준 "발행 건수와 저장 건수가 일치"). 그래서
// quantity - SUM(이 주문이 buy_order_id 또는 sell_order_id인 execution.quantity)
// 는 그 주문의 remaining_quantity가 실제로 얼마여야 하는지에 대한 절대값이자
// 유일하게 진실인 값입니다 — applyFillsBatch가 이미 정확히 반영해둔 주문을 다시
// 계산해도 같은 값이 나오므로, 매 InsertOrdersBatch 호출마다(주문이 실제로
// 새로 INSERT됐든 INSERT IGNORE로 스킵된 재전달이든) 무조건 호출해도
// 안전합니다. 한 주문은 항상 한쪽 side로만 고정되므로(BUY 주문이 나중에
// sell_order_id로 나타날 수 없음) 두 IN 목록에 같은 주문이 동시에 매치되는
// 경우는 없습니다.
//
// status/canceled_at 갱신 로직은 applyFillsBatch와 동일한 우선순위를 씁니다 —
// 이번 반영으로 완전 체결이 확정되면 기존 CANCELED를 정정하고, 아니면 기존
// CANCELED를 보존합니다(주석 위치는 applyFillsBatch 정의부 참고).
func reconcilePreexistingFills(ctx context.Context, tx *sql.Tx, orderIDs []string) error {
	if len(orderIDs) == 0 {
		return nil
	}

	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(orderIDs)), ",")
	args := make([]any, 0, len(orderIDs)*2)
	for _, id := range orderIDs {
		args = append(args, id)
	}
	args = append(args, args...) // 아래 두 IN(...) 절 각각에 같은 목록이 필요

	rows, err := tx.QueryContext(ctx, fmt.Sprintf(`
		SELECT order_id, SUM(qty) FROM (
			SELECT buy_order_id AS order_id, quantity AS qty FROM execution WHERE buy_order_id IN (%s)
			UNION ALL
			SELECT sell_order_id AS order_id, quantity AS qty FROM execution WHERE sell_order_id IN (%s)
		) matched
		GROUP BY order_id
	`, placeholders, placeholders), args...)
	if err != nil {
		return fmt.Errorf("선도착 체결 조회 실패: %w", err)
	}

	type fill struct{ orderID, total string }
	var fills []fill
	for rows.Next() {
		var f fill
		if err := rows.Scan(&f.orderID, &f.total); err != nil {
			rows.Close()
			return fmt.Errorf("선도착 체결 조회 실패: %w", err)
		}
		fills = append(fills, f)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("선도착 체결 조회 실패: %w", err)
	}
	rows.Close()

	for _, f := range fills {
		if _, err := tx.ExecContext(ctx, `
			UPDATE trade_order
			SET remaining_quantity = quantity - ?,
			    status = CASE
			        WHEN quantity - ? <= 0 THEN 'FILLED'
			        WHEN status = 'CANCELED' THEN 'CANCELED'
			        ELSE 'PARTIALLY_FILLED'
			    END,
			    canceled_at = CASE WHEN quantity - ? <= 0 THEN NULL ELSE canceled_at END
			WHERE order_id = ?
		`, f.total, f.total, f.total, f.orderID); err != nil {
			return fmt.Errorf("선도착 체결 반영 실패 (orderId=%s): %w", f.orderID, err)
		}
	}
	return nil
}

// CancelOrdersBatch는 취소 여러 건을 order_id→canceled_at 매핑을 담은 파생
// 테이블(VALUES)과 JOIN해서 한 번의 UPDATE...JOIN으로 반영합니다.
//
// **2026-08-21, executions 배치화와 같은 이유로 재작성** — 이전엔 취소 건마다
// 개별 UPDATE를 트랜잭션 하나로만 묶어 실행했습니다(200건이면 UPDATE 200번,
// applyFillsBatch가 겪었던 것과 같은 구조적 병목의 축소판). 각 취소가 서로
// 다른 order_id·canceled_at을 대상으로 해서 정적인 CASE WHEN으로는 못 묶지만,
// order_id→canceled_at 매핑 자체를 파생 테이블로 만들어 JOIN하면 한 문장으로
// 묶을 수 있습니다. 대상 주문이 없는 항목(NEW를 못 본 CANCEL)은 JOIN이 그냥
// 매치되지 않아 조용히 넘어갑니다 — 에러가 아닙니다.
//
// **같은 order_id가 한 배치 안에 두 번 이상 등장하면 첫 번째 항목만 반영합니다**
// — 정상 흐름에선 거의 없지만(재전달 등으로 이론상 가능), 기존 순차 처리에서는
// 첫 취소가 먼저 status를 CANCELED로 바꾸고 나면 같은 order_id의 후속 취소는
// 아래 WHERE 가드에 걸려 0건 영향(=무시)이었습니다. 이 결과를 그대로 재현하려고
// Go 쪽에서 미리 결정적으로 중복을 제거합니다 — MySQL의 다중 매치 UPDATE...JOIN이
// 어느 쪽 값을 적용할지는 명세상 보장되지 않아서, 그 판단을 SQL에 맡기지 않습니다.
//
// WHERE에 FILLED도 막는 이유(2026-08-07, 실제 발생 가능한 케이스 분석 후 결정) —
// orders/executions는 기록기가 서로 독립적으로 소비하므로(배칭으로 그 창이 더
// 벌어짐), 어떤 주문이 완전 체결된 *뒤에* 그 주문에 대한 취소가 기록기에 도착할
// 수 있습니다(가장 흔한 경로: orderapi의 order.Store가 체결 결과를 몰라 이미
// 체결된 주문의 취소 요청도 그냥 받아서 발행해버림 — server.go의 "still never
// learns about fills" 참고). 완전 체결은 매칭엔진의 호가창에서 이미 확정된
// 사실이라 뒤늦은 취소가 그걸 덮어쓰면 안 됩니다 — applyFillsBatch가 반대 방향
// (체결이 취소를 정정)을 처리하는 것과 대칭되는 보호입니다.
func (s *MySQLStore) CancelOrdersBatch(ctx context.Context, cancels []CancelInput) error {
	if len(cancels) == 0 {
		return nil
	}

	seen := make(map[string]struct{}, len(cancels))
	var sb strings.Builder
	sb.WriteString(`
		UPDATE trade_order t
		JOIN (`)
	args := make([]any, 0, len(cancels)*2)
	first := true
	for _, c := range cancels {
		if _, ok := seen[c.OrderID]; ok {
			continue
		}
		seen[c.OrderID] = struct{}{}

		parsed, err := parseTimestamp(c.CanceledAt)
		if err != nil {
			return fmt.Errorf("취소 일괄 반영 실패 (orderId=%s): %w", c.OrderID, err)
		}
		if !first {
			sb.WriteString(" UNION ALL ")
		}
		first = false
		sb.WriteString("SELECT ? AS order_id, ? AS canceled_at")
		args = append(args, c.OrderID, parsed)
	}
	sb.WriteString(`
		) d ON t.order_id = d.order_id
		SET t.status = 'CANCELED', t.canceled_at = d.canceled_at
		WHERE t.status NOT IN ('CANCELED', 'FILLED')
	`)

	return withRetryOnDeadlock(func() error {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("취소 일괄 반영 실패: %w", err)
		}
		defer tx.Rollback()

		if _, err := tx.ExecContext(ctx, sb.String(), args...); err != nil {
			return fmt.Errorf("취소 일괄 반영 실패 (%d건): %w", len(cancels), err)
		}

		if err := tx.Commit(); err != nil {
			return fmt.Errorf("취소 일괄 반영 실패 (커밋): %w", err)
		}
		return nil
	})
}

// ApplyExecutionsBatch는 체결 여러 건을 한 트랜잭션(=한 번의 커밋) 안에서:
// mode는 fetchModes로 한 번에 조회하고, remaining_quantity/status는
// applyFillsBatch로 한 번에 합산 반영한 뒤, execution 행들은 한 번의 다중 행
// INSERT로 저장합니다.
//
// **2026-08-21, 구조적 병목 재작성**: 이전엔 체결 건마다 매수/매도 각각
// updateFill을 개별 호출해서(건당 UPDATE 1번 + 찾았으면 SELECT 1번) 200건
// 배치에 최대 800번의 순차 DB 왕복이 발생했습니다. recorder_consumer_lag가
// orders는 0인데 executions만 폭증하는 실측 원인이 이것이었고, 배치/레플리카를
// 늘려도(파티션 기반 수평 확장이라 레플리카별 처리량 자체는 그대로) 근본
// 해결이 안 됐습니다. mode 조회를 IN(...) 쿼리 1번으로, 잔량 차감을
// UPDATE...JOIN 1번(order_id별 SUM으로 합산)으로 묶어 200건 배치 기준 왕복을
// 800번에서 사실상 4번(mode 조회 1 + 중복 확인 1 + 잔량 UPDATE 1 + execution
// INSERT 1)으로 줄였습니다. 트랜잭션 하나로 묶이는 것 자체(배치당 커밋 1번)는
// 기존과 동일. 반환값은 execs와 같은 순서·같은 길이입니다.
//
// **2026-08-24, 배치 재처리(재전달) 안전성 확보 — 실제 사고 대응.** recorder가
// DB 커밋은 성공했는데 그 배치의 Kafka 오프셋 커밋 전에 죽으면(재배포/스케일
// 아웃 중 다수 파드가 동시에 뜨면서 MySQL/Kafka 초기 연결이 몰려 실패하는
// 식으로 실제 발생 — main()의 접속 실패 시 즉시 log.Fatal하는 구조라 크래시
// 루프로 이어짐), 재시작 후 그 배치가 통째로 다시 처리됩니다. execution은
// 매번 idgen으로 새 ID를 만들어 INSERT하기 때문에(trade_order의 INSERT IGNORE
// 같은 자연 키 보호가 없었음) 완전히 똑같은 체결이 새 ID로 한 번 더 쌓였고,
// 같은 트랜잭션 안에서 applyFillsBatch도 같이 재실행되면서 remaining_quantity가
// 상대 차감(- d.qty_sum) 방식이라 두 번 깎여버렸습니다(실제 발견: 2026-08-24,
// 대시보드 정합성 검사의 "중복 체결" 지표로 처음 포착). 고친 방식: execution에
// (buy_order_id, sell_order_id) 유니크 인덱스를 추가하고(schema.sql의
// uq_execution_order_pair — 가격-시간 우선순위 매칭 특성상 같은 두 주문은
// 논리적으로 한 번만 매칭될 수 있어 자연 키로 안전함) INSERT를 IGNORE로
// 바꿨습니다. 그것만으로는 execution 중복 저장만 막고 잔량 이중 차감은 못
// 막으므로(INSERT IGNORE와 applyFillsBatch는 서로 다른 문장), 인서트 전에
// 이 배치의 쌍들이 이미 저장돼 있는지 먼저 확인해서, 이미 있는 쌍은
// applyFillsBatch 대상에서 아예 빼버립니다 — "이미 반영됐던 체결"은 잔량
// 계산에 다시 참여하지 않습니다.
func (s *MySQLStore) ApplyExecutionsBatch(ctx context.Context, execs []ExecutionInput) ([]ExecutionResult, error) {
	if len(execs) == 0 {
		return nil, nil
	}

	orderIDs := make([]string, 0, len(execs)*2)
	for _, in := range execs {
		orderIDs = append(orderIDs, in.BuyOrderID, in.SellOrderID)
	}

	var results []ExecutionResult
	err := withRetryOnDeadlock(func() error {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("체결 일괄 반영 실패: %w", err)
		}
		defer tx.Rollback()

		modeByOrder, err := fetchModes(ctx, tx, orderIDs)
		if err != nil {
			return fmt.Errorf("체결 일괄 반영 실패 (mode 조회): %w", err)
		}

		alreadyRecorded, err := fetchExistingExecutionPairs(ctx, tx, execs)
		if err != nil {
			return fmt.Errorf("체결 일괄 반영 실패 (중복 확인): %w", err)
		}

		newExecs := make([]ExecutionInput, 0, len(execs))
		for _, in := range execs {
			if !alreadyRecorded[in.BuyOrderID+"|"+in.SellOrderID] {
				newExecs = append(newExecs, in)
			}
		}
		if len(newExecs) > 0 {
			if err := applyFillsBatch(ctx, tx, newExecs); err != nil {
				return fmt.Errorf("체결 일괄 반영 실패 (잔량 갱신): %w", err)
			}
		}

		results = make([]ExecutionResult, len(execs))
		var sb strings.Builder
		sb.WriteString(`INSERT IGNORE INTO execution (execution_id, market_code, buy_order_id, sell_order_id, price, quantity, mode, executed_at) VALUES `)
		args := make([]any, 0, len(execs)*7)

		for i, in := range execs {
			buyMode, buyFound := modeByOrder[in.BuyOrderID]
			sellMode, sellFound := modeByOrder[in.SellOrderID]
			mode, mismatched := ResolveMode(buyMode, buyFound, sellMode, sellFound)
			execID := idgen.NewExecutionID()

			if i > 0 {
				sb.WriteString(",")
			}
			// UTC_TIMESTAMP()(인자 없음)는 밀리초를 아예 버리고 초 단위로
			// 저장합니다 — executed_at 컬럼 자체는 DATETIME(3)인데 소스 값이
			// 이미 .000으로 잘려서 들어오니, 같은 초 안에 접수된 submitted_at
			// (orderapi가 밀리초까지 찍음)보다 실제로는 나중인 체결이 숫자
			// 비교에서 더 이르게 보이는 "순서 역전" 오탐이 실측으로 확인됐습니다
			// (2026-08-26, 실 DB 위반 행 20건 전부 이 패턴과 일치). UTC_TIMESTAMP(3)로
			// 밀리초까지 유지해서 고칩니다.
			sb.WriteString("(?, ?, ?, ?, ?, ?, ?, UTC_TIMESTAMP(3))")
			args = append(args, execID, in.Market, in.BuyOrderID, in.SellOrderID, in.Price, in.Quantity, nullIfEmpty(mode))

			results[i] = ExecutionResult{
				ExecutionID: execID, Mode: mode,
				BuyFound: buyFound, SellFound: sellFound, ModeMismatched: mismatched,
				Inserted: !alreadyRecorded[in.BuyOrderID+"|"+in.SellOrderID],
			}
		}

		res, err := tx.ExecContext(ctx, sb.String(), args...)
		if err != nil {
			return fmt.Errorf("체결 일괄 저장 실패 (%d건): %w", len(execs), err)
		}
		// INSERT IGNORE는 재전달로 스킵된 중복을 RowsAffected에서 제외합니다 —
		// len(newExecs)와 이론상 같은 값이어야 하지만(위 alreadyRecorded 판정과
		// 동일 기준), 실제 반영된 행 수를 그대로 신뢰하는 쪽이 안전합니다
		// (InsertOrdersBatch와 같은 이유).
		if _, err := res.RowsAffected(); err != nil {
			return fmt.Errorf("영향받은 행 수 확인 실패: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("체결 일괄 반영 실패 (커밋): %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return results, nil
}

// fetchExistingExecutionPairs는 이 배치의 (buy_order_id, sell_order_id) 쌍
// 중 execution 테이블에 이미 저장돼 있는 게 있는지 확인합니다 — 배치 전체가
// 재처리되는 상황(recorder가 DB 커밋 후 Kafka 오프셋 커밋 전에 죽었다가
// 재시작하는 경우, 2026-08-24 실측)에서 applyFillsBatch가 잔량을 두 번
// 차감하지 않도록 걸러내는 용도입니다. uq_execution_order_pair 유니크
// 인덱스를 그대로 타므로 배치 크기가 커져도 저렴합니다.
func fetchExistingExecutionPairs(ctx context.Context, tx *sql.Tx, execs []ExecutionInput) (map[string]bool, error) {
	var sb strings.Builder
	sb.WriteString(`
		SELECT e.buy_order_id, e.sell_order_id
		FROM execution e
		JOIN (`)
	args := make([]any, 0, len(execs)*2)
	for i, in := range execs {
		if i > 0 {
			sb.WriteString(" UNION ALL ")
		}
		sb.WriteString("SELECT ? AS buy_order_id, ? AS sell_order_id")
		args = append(args, in.BuyOrderID, in.SellOrderID)
	}
	sb.WriteString(`
		) batch ON e.buy_order_id = batch.buy_order_id AND e.sell_order_id = batch.sell_order_id
	`)

	rows, err := tx.QueryContext(ctx, sb.String(), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	existing := make(map[string]bool)
	for rows.Next() {
		var buy, sell string
		if err := rows.Scan(&buy, &sell); err != nil {
			return nil, err
		}
		existing[buy+"|"+sell] = true
	}
	return existing, rows.Err()
}

// fetchModes는 이 배치가 참조하는 주문 id들(매수+매도, 중복 가능)의 mode를
// 한 번의 IN(...) 쿼리로 조회합니다 — 기존 updateFill이 체결 건마다 개별
// SELECT를 날리던 것(200건 배치에 최대 400번)을 대체합니다. 반환 맵에 키가
// 없으면 그 주문의 NEW 이벤트를 아직 못 봤다는 뜻으로, 기존 updateFill의
// found=false와 동일하게 취급합니다.
func fetchModes(ctx context.Context, tx *sql.Tx, orderIDs []string) (map[string]string, error) {
	seen := make(map[string]struct{}, len(orderIDs))
	unique := make([]string, 0, len(orderIDs))
	for _, id := range orderIDs {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		unique = append(unique, id)
	}

	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(unique)), ",")
	args := make([]any, len(unique))
	for i, id := range unique {
		args[i] = id
	}

	rows, err := tx.QueryContext(ctx, fmt.Sprintf(`SELECT order_id, mode FROM trade_order WHERE order_id IN (%s)`, placeholders), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[string]string, len(unique))
	for rows.Next() {
		var id, mode string
		if err := rows.Scan(&id, &mode); err != nil {
			return nil, err
		}
		result[id] = mode
	}
	return result, rows.Err()
}

// applyFillsBatch는 이 배치의 모든 체결(매수+매도)이 각 주문에 미치는
// remaining_quantity 차감을 order_id별로 SQL(SUM/GROUP BY)에서 합산한 뒤
// 한 번의 UPDATE...JOIN으로 반영합니다 — 기존 updateFill이 체결 건마다
// 개별 UPDATE를 날리던 것(200건 배치에 최대 400번의 순차 UPDATE)을
// 대체합니다. 잔량 비교/차감을 전부 SQL DECIMAL 연산으로 하므로 Go 쪽에
// decimal 라이브러리를 새로 들일 필요가 없습니다(recorder는 지금까지
// 이 산술을 전부 SQL에 맡겨온 기존 관례 그대로).
//
// **순차 처리와 결과가 동일한 이유(결합법칙)**: 같은 주문에 대한 차감
// d1,d2,...,dN을 순서대로 적용하나 합산 D=d1+...+dN을 한 번에 적용하나
// 최종 remaining_quantity는 항상 rq0-D로 같고, status/canceled_at 판정도
// 최종 (rq0-D<=0) 여부에만 의존합니다(완전체결로 확정된 뒤엔 추가 차감이
// 있어도 판정이 안 바뀜 — updateFill의 CASE 우선순위 설계와 동일: ① 이번
// 반영으로 잔량이 0 이하가 되면 기존 CANCELED도 FILLED로 정정, ② 아직
// 남았는데 CANCELED면 보존, ③ 그 외엔 PARTIALLY_FILLED, canceled_at도 함께
// 정리). 한 주문은 항상 한쪽 side로만 고정되므로(reconcilePreexistingFills
// 주석 참고) 이 배치 안에서 order_id가 중복되는 건 "같은 주문이 여러 체결에
// 걸쳐 부분체결"된 경우뿐이고, 위 결합법칙 덕분에 문제없이 합산됩니다.
// 대상 주문이 없으면(NEW를 못 본 체결) JOIN이 그냥 매치되지 않아 조용히
// 건너뜁니다 — 에러가 아닙니다.
func applyFillsBatch(ctx context.Context, tx *sql.Tx, execs []ExecutionInput) error {
	type fillRow struct{ orderID, qty string }
	rows := make([]fillRow, 0, len(execs)*2)
	for _, in := range execs {
		rows = append(rows, fillRow{in.BuyOrderID, in.Quantity}, fillRow{in.SellOrderID, in.Quantity})
	}

	var sb strings.Builder
	sb.WriteString(`
		UPDATE trade_order t
		JOIN (
			SELECT order_id, SUM(qty) AS qty_sum
			FROM (`)
	args := make([]any, 0, len(rows)*2)
	for i, r := range rows {
		if i > 0 {
			sb.WriteString(" UNION ALL ")
		}
		sb.WriteString("SELECT ? AS order_id, CAST(? AS DECIMAL(24,8)) AS qty")
		args = append(args, r.orderID, r.qty)
	}
	sb.WriteString(`
			) x
			GROUP BY order_id
		) d ON t.order_id = d.order_id
		SET t.remaining_quantity = t.remaining_quantity - d.qty_sum,
		    t.status = CASE
		        WHEN t.remaining_quantity - d.qty_sum <= 0 THEN 'FILLED'
		        WHEN t.status = 'CANCELED' THEN 'CANCELED'
		        ELSE 'PARTIALLY_FILLED'
		    END,
		    t.canceled_at = CASE WHEN t.remaining_quantity - d.qty_sum <= 0 THEN NULL ELSE t.canceled_at END
	`)
	_, err := tx.ExecContext(ctx, sb.String(), args...)
	return err
}

// AssignMarket은 FR-11 배정 이벤트를 새 행으로 기록합니다(released_at은 아직 NULL).
// 먼저 이 마켓에 아직 열려 있는(released_at IS NULL) 배정이 있으면 닫습니다 —
// 정상적인 경우 이전 담당자의 Release가 이미 닫아놔서 여기선 0건에 영향을 줄
// 뿐이지만, 이전 담당자가 강제 종료 등으로 RELEASED를 못 보낸 경우(실제 검증
// 중 발견 — 오래된 open 행이 영원히 남아 "released_at IS NULL로 지금 담당자를
// 알 수 있다"는 이 테이블의 존재 목적 자체가 깨졌었음) 새 ASSIGNED가 유일한
// 진짜 담당자라는 걸 알 수 있습니다 — Kafka 그룹의 Generation.Start 계약("새
// 배정을 받았다는 건 이전 담당자가 이미 완전히 멈췄다")이 이 가정을 보장합니다.
//
// **INSERT IGNORE, 2026-08-24** — execution과 같은 배치 재전달 문제(이쪽은
// 건당 처리라 "배치"는 아니지만, 이 함수 하나의 커밋과 그 이벤트의 Kafka
// 오프셋 커밋 사이에 크래시가 나면 재전달로 이 함수 전체가 다시 호출될 수
// 있음)에 대한 대비입니다. 재호출되면 새 assignment_id로 감사 이력에 중복
// 행이 하나 더 쌓이긴 하지만(released_at IS NULL 판정 자체는 자기 자신을
// 다시 닫았다가 새로 여는 식으로 결국 회복돼 서비스 상태는 안 깨짐) 이력이
// 지저분해지는 건 막을 이유가 있어, uq_assignment_market_engine_assigned
// (market_code, engine_instance_id, assigned_at) 유니크 인덱스 + INSERT
// IGNORE로 막습니다 — 같은 엔진이 같은 마켓을 밀리초 단위로 똑같은 시각에
// 두 번 "새로" 배정받는 건 재전달이 아니고서야 있을 수 없는 조합입니다.
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
		INSERT IGNORE INTO matching_engine_assignment (assignment_id, market_code, engine_instance_id, assigned_at)
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
