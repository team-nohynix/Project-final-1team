// Package store는 docs/erd.md의 TRADE_ORDER/EXECUTION 테이블에 대한 쓰기를
// 다룹니다. PostgresStore가 실제 구현이고, 이 인터페이스는 apply.go의 오케스트레이션
// 로직을 실제 DB 없이 테스트하기 위해 존재합니다(orderapi/kafkaclient.Publisher와
// 같은 패턴).
package store

import "context"

// NewOrder는 orders 토픽의 NEW 이벤트에서 저장할 필드입니다. SourceOrderID는
// 리플레이 주문만 값이 있고(trader의 신규 주문은 빈 문자열), TRADE_ORDER의
// 자기참조 컬럼 source_order_id(docs/erd.md)로 저장됩니다.
type NewOrder struct {
	OrderID         string
	ClientRequestID string
	Market          string
	Side            string
	Price           string
	Quantity        string
	Mode            string
	SubmittedAt     string
	SourceOrderID   string
}

// CancelInput은 orders 토픽의 CANCEL 이벤트에서 저장할 필드입니다.
type CancelInput struct {
	OrderID    string
	CanceledAt string
}

// ExecutionInput은 executions 토픽 이벤트에서 저장할 필드입니다.
type ExecutionInput struct {
	Market      string
	BuyOrderID  string
	SellOrderID string
	Price       string
	Quantity    string
}

// AssignmentInput은 assignments 토픽 이벤트(FR-11)에서 저장할 필드입니다.
type AssignmentInput struct {
	Market           string
	EngineInstanceID string
	At               string
}

// ExecutionResult는 ApplyExecution이 실제로 한 일을 보고합니다 — apply.go가
// 이 값을 보고 경고 로그를 남길지 결정합니다.
type ExecutionResult struct {
	ExecutionID string
	Mode        string
	// BuyFound/SellFound는 각 주문을 실제로 찾았는지입니다(false면 기록기가
	// 그 주문의 NEW 이벤트를 못 봤다는 뜻 — 기록기가 스트림 중간에 시작된 경우 등).
	// execution 행 자체는 이 값과 무관하게 항상 저장됩니다(FR-09 검증 기준:
	// "Kafka 발행 건수와 DB 저장 건수가 일치").
	BuyFound       bool
	SellFound      bool
	ModeMismatched bool
}

// Store는 TRADE_ORDER/EXECUTION에 대한 쓰기를 추상화합니다. InsertOrdersBatch/
// CancelOrdersBatch/ApplyExecutionsBatch는 RDS 백프레셔 대응(2026-08-07,
// CLAUDE.md의 "RDS admission control via recorder consumer lag" 참고)으로
// 메시지 1건당 DB 왕복 1번이던 것을 여러 건을 모아 한 번에 쓰도록 바꾼
// 것입니다 — 옛 단건 메서드(InsertOrder/CancelOrder/ApplyExecution)는 배치
// 크기 1로도 완전히 동일하게 동작하므로 별도로 남겨두지 않고 이걸로 대체했습니다.
type Store interface {
	// InsertOrdersBatch는 신규 주문 여러 건을 한 번의 다중 행 INSERT로
	// 저장합니다. 같은 order_id가 이미 있으면(재시작 후 컨슈머 그룹의
	// at-least-once 재전달 등) 그 행만 조용히 무시합니다(INSERT IGNORE).
	InsertOrdersBatch(ctx context.Context, orders []NewOrder) error
	// CancelOrdersBatch는 취소 여러 건을 한 트랜잭션(한 번의 커밋) 안에서
	// 처리합니다. 대상 주문이 없는 항목(NEW를 못 본 CANCEL)은 그 항목만
	// 조용히 건너뜁니다 — 에러가 아닙니다.
	CancelOrdersBatch(ctx context.Context, cancels []CancelInput) error
	// ApplyExecutionsBatch는 체결 여러 건을 한 트랜잭션(한 번의 커밋) 안에서:
	// 각 체결의 매수/매도 양쪽 주문 remaining_quantity/status를 갱신하고
	// (존재하는 쪽만), execution 행들을 한 번의 다중 행 INSERT로 저장합니다.
	// 반환되는 []ExecutionResult는 입력 execs와 같은 순서·같은 길이입니다.
	ApplyExecutionsBatch(ctx context.Context, execs []ExecutionInput) ([]ExecutionResult, error)
	// AssignMarket은 FR-11 배정 이벤트를 기록합니다(released_at=NULL인 새 행 추가).
	// assignments 토픽은 물량이 적어(리밸런스 시에만 발생) 배칭하지 않습니다.
	AssignMarket(ctx context.Context, in AssignmentInput) error
	// ReleaseMarket은 해당 마켓·인스턴스의 열려 있는(released_at IS NULL) 배정을
	// 반납 처리합니다. 그런 배정이 없으면(예: ASSIGNED 이벤트를 못 본 경우) 아무
	// 일도 하지 않습니다 — 에러가 아닙니다.
	ReleaseMarket(ctx context.Context, in AssignmentInput) error
}
