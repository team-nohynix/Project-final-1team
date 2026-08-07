package kafkaclient

import (
	"context"
	"encoding/json"
	"time"

	kafka "github.com/segmentio/kafka-go"

	"matching/orderbook"
)

// executionMessage는 executions 토픽에 싣는 메시지 모양입니다. orderapi의 orderEvent와
// 마찬가지로 FR-22 규격 문서가 없어 잠정입니다 — "기록기" 쪽과 나중에 맞춰야 합니다.
type executionMessage struct {
	Market      string `json:"market"`
	BuyOrderID  string `json:"buyOrderId"`
	SellOrderID string `json:"sellOrderId"`
	Price       string `json:"price"`
	Quantity    string `json:"quantity"`
}

// ExecutionProducer는 체결 결과를 executions 토픽에 발행합니다(FR-09 중 Kafka 발행
// 부분 — DB 저장은 "기록기"가 executions를 구독해서 하는 것이라 여기 책임이 아닙니다).
// orderapi/kafkaclient.go의 OrderProducer와 같은 이유로 key=market, &kafka.Hash{} 밸런서를
// 씁니다(같은 마켓 체결은 항상 같은 파티션에 순서대로 쌓여야 함, NFR-07).
type ExecutionProducer struct {
	writer *kafka.Writer
}

func NewExecutionProducer(broker, topic string) *ExecutionProducer {
	return &ExecutionProducer{
		writer: &kafka.Writer{
			Addr:                   kafka.TCP(broker),
			Topic:                  topic,
			Balancer:               &kafka.Hash{},
			BatchTimeout:           10 * time.Millisecond,
			AllowAutoTopicCreation: true,
		},
	}
}

// Publish는 engine.ExecutionPublisher 인터페이스를 만족합니다.
func (p *ExecutionProducer) Publish(ctx context.Context, exec orderbook.Execution) error {
	body, err := json.Marshal(executionMessage{
		Market:      exec.Market,
		BuyOrderID:  exec.BuyOrderID,
		SellOrderID: exec.SellOrderID,
		Price:       exec.Price.String(),
		Quantity:    exec.Quantity.String(),
	})
	if err != nil {
		return err
	}
	return p.writer.WriteMessages(ctx, kafka.Message{Key: []byte(exec.Market), Value: body})
}

func (p *ExecutionProducer) Close() error {
	return p.writer.Close()
}
