package kafkaclient

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	kafka "github.com/segmentio/kafka-go"
)

// executionMessage는 executions 토픽 메시지 모양입니다. matching/kafkaclient의
// producer.go가 쓰는 것과 byte-identical해야 합니다(모듈 간 타입 비공유
// 원칙 — 값만 맞추면 됨). recorder/events가 이미 같은 모양을 독립적으로
// 재선언한 것과 같은 이유입니다.
type executionMessage struct {
	Market      string `json:"market"`
	BuyOrderID  string `json:"buyOrderId"`
	SellOrderID string `json:"sellOrderId"`
	Price       string `json:"price"`
	Quantity    string `json:"quantity"`
}

// ExecutionEvent는 executions 토픽 메시지 하나를 디코딩한 것입니다.
type ExecutionEvent struct {
	Market      string
	BuyOrderID  string
	SellOrderID string
	Price       string
	Quantity    string
}

// ExecutionConsumer는 executions 토픽을 구독해 order.Store가 체결을 반영할 수
// 있게 합니다(2026-08-10 — 이전에는 아무도 executions를 orderapi 쪽으로
// 소비하지 않아서 cancelOrderHandler의 ORDER_ALREADY_FILLED 체크가 무의미했음,
// CLAUDE.md 참고). matching의 FR-11 GroupConsumer 같은 복잡한 재분배 장치가
// 필요 없습니다 — order.Store에는 파티션 리밸런스로부터 지켜야 할 상태가 없고
// (recorder의 리더들과 같은 이유), orderapi는 항상 단일 인스턴스로만 돕니다.
// 그래서 평범한 kafka.NewReader(GroupID:...) + FetchMessage/CommitMessages면
// 충분합니다(recorder/kafka의 리더들과 같은 패턴).
type ExecutionConsumer struct {
	reader *kafka.Reader
}

// useIAM이 false면(로컬 dev-kafka) 인증 없이 붙고, true면(MSK) AWS_MSK_IAM+TLS로
// 인증합니다(auth.go 참고).
func NewExecutionConsumer(ctx context.Context, broker, topic, groupID string, useIAM bool) (*ExecutionConsumer, error) {
	dialer, err := newDialer(ctx, useIAM)
	if err != nil {
		return nil, fmt.Errorf("Kafka SASL 메커니즘 생성 실패: %w", err)
	}
	return &ExecutionConsumer{reader: kafka.NewReader(kafka.ReaderConfig{
		Brokers: []string{broker},
		Topic:   topic,
		Dialer:  dialer,
		GroupID: groupID,
	})}, nil
}

// Run은 ctx가 끝나거나 에러가 날 때까지 executions를 계속 소비합니다.
// FetchMessage(즉시 커밋 안 함)+handle+CommitMessages 순서를 씁니다 —
// kafka.Reader.ReadMessage는 handle 실행 전에 이미 커밋해버려서, 처리 중
// 실패하면 재시도 기회 없이 메시지가 영원히 스킵됩니다(recorder에서 실제로
// 겪은 문제, CLAUDE.md 참고). 디코딩 실패(메시지 자체가 손상됨 — 재시도해도
// 못 고침)는 로그만 남기고 커밋해 넘어갑니다. handle 자체는 지금 설계상 거의
// 에러를 내지 않습니다 — order.Store.ApplyFill은 주문을 못 찾아도 에러가
// 아니라 found=false만 돌려주는 설계이기 때문입니다(orderapi 재시작으로
// 인메모리 Store가 비어 있는 상태에서 예전 주문의 체결이 뒤늦게 와도 정상
// 동작). 그럼에도 handle이 에러를 반환하면 커밋하지 않고 Run을 멈춥니다 —
// 호출부(main.go)가 재시작하면 마지막 커밋 지점부터 다시 시도합니다.
func (c *ExecutionConsumer) Run(ctx context.Context, handle func(ctx context.Context, ev ExecutionEvent) error) error {
	for {
		msg, err := c.reader.FetchMessage(ctx)
		if err != nil {
			return fmt.Errorf("executions 토픽 읽기 실패: %w", err)
		}

		var raw executionMessage
		if err := json.Unmarshal(msg.Value, &raw); err != nil {
			log.Printf("executions 메시지 디코딩 실패, 건너뜀: %v", err)
			if err := c.reader.CommitMessages(ctx, msg); err != nil {
				log.Printf("executions 오프셋 커밋 실패: %v", err)
			}
			continue
		}

		ev := ExecutionEvent{
			Market:      raw.Market,
			BuyOrderID:  raw.BuyOrderID,
			SellOrderID: raw.SellOrderID,
			Price:       raw.Price,
			Quantity:    raw.Quantity,
		}
		if err := handle(ctx, ev); err != nil {
			return fmt.Errorf("executions 이벤트 처리 실패 (market=%s): %w", ev.Market, err)
		}

		if err := c.reader.CommitMessages(ctx, msg); err != nil {
			return fmt.Errorf("executions 오프셋 커밋 실패 (market=%s): %w", ev.Market, err)
		}
	}
}

func (c *ExecutionConsumer) Close() error {
	return c.reader.Close()
}
