// Package kafkaclient는 orderapi가 orders 토픽에 주문 이벤트를 발행하는 부분을 담당합니다.
package kafkaclient

import (
	"context"
	"encoding/json"
	"time"

	kafka "github.com/segmentio/kafka-go"

	"orderapi/order"
)

// Publisher는 주문 이벤트를 어딘가에 발행합니다. 핸들러는 이 인터페이스만 알고,
// 실제 구현(OrderProducer)은 몰라도 됩니다 — 테스트에서는 실제 Kafka 없이
// 가짜 구현체를 주입할 수 있습니다.
type Publisher interface {
	PublishNew(ctx context.Context, o *order.Order, clientRequestID, mode, sourceOrderID string) error
	PublishCancel(ctx context.Context, orderID, market, canceledAt string) error
}

// OrderProducer는 주문 이벤트를 Kafka orders 토픽에 발행하는 Publisher 구현체입니다.
//
// backend/kafka/producer.go(삭제됨)가 쓰던 &kafka.LeastBytes{}는 메시지 Key를 무시하고
// 파티션별 누적 바이트량만으로 파티션을 고르는 전략이라 "같은 마켓은 항상 같은 파티션"
// (FR-04, 순서 보장 NFR-07)을 보장하지 못했습니다. 한동안 &kafka.Hash{}를 썼는데,
// 이건 Key를 해시해서 파티션을 고르므로 "같은 마켓은 항상 같은 파티션"은 지켜지지만
// 해시 충돌이 나면 "마켓 1개 = 파티션 1개"라는 더 강한 보장(FR-11 마켓 재분배가 전제)은
// 못 지킵니다. 그래서 커스텀 marketPartitioner(balancer.go)로 교체 — 해시가 아니라
// TargetMarkets 리스트에서의 인덱스를 그대로 파티션 번호로 씁니다.
type OrderProducer struct {
	writer *kafka.Writer
}

// NewOrderProducer는 topic에 발행하는 Publisher를 만듭니다.
func NewOrderProducer(broker, topic string) *OrderProducer {
	return &OrderProducer{
		writer: &kafka.Writer{
			Addr:         kafka.TCP(broker),
			Topic:        topic,
			Balancer:     marketPartitioner{},
			BatchTimeout: 10 * time.Millisecond,
			// 로컬 dev-kafka(KAFKA_AUTO_CREATE_TOPICS_ENABLE=true)에 맞춰 토픽이 없으면
			// 자동 생성을 요청합니다. kafka-go의 기본값은 false라 브로커가 자동 생성을
			// 허용해도 클라이언트가 따로 켜주지 않으면 "Unknown Topic Or Partition"으로
			// 실패합니다(실제로 이 버그를 겪고 알게 됨). prod에서 토픽을 미리 만들어
			// 관리하게 되면 이 옵션은 굳이 없어도 되지만, 켜져 있어도 무해합니다.
			AllowAutoTopicCreation: true,
		},
	}
}

// orderEvent는 Kafka orders 토픽에 싣는 메시지 모양입니다. FR-22가 말하는
// "별도의 Kafka 메시지 규격 문서"가 아직 없어 최소한의 모양으로 정한 것 —
// 그 문서가 생기면 그쪽 기준으로 맞춰야 합니다(CLAUDE.md에 기록).
//
// ClientRequestID는 요청의 Idempotency-Key 값입니다. order.Order 자체(HTTP 응답
// 바디로도 그대로 나가는 구조체)에는 담지 않고 이 메시지에만 실어 보냅니다 —
// docs/api-specification.md가 정의한 응답 바디 계약을 건드리지 않으면서, 나중에
// "기록기"가 orders 토픽을 구독해 TRADE_ORDER.client_request_id(docs/erd.md)를
// 채울 수 있게 하려는 목적입니다. Mode(NEW만)/CanceledAt(CANCEL만)도 같은 이유로
// 추가됐습니다 — TRADE_ORDER.mode/canceled_at을 기록기가 채울 수 있게 하는 배관.
// SourceOrderID(NEW만)도 같은 패턴 — replayengine이 요청 바디의 sourceOrderId로
// 실어 보낸 원본 페이퍼 트레이딩 주문 번호를 그대로 옮겨, 기록기가
// TRADE_ORDER.source_order_id를 채울 수 있게 합니다(trader의 신규 주문은 이
// 필드가 항상 빈 문자열). matching은 이 필드들을 몰라도 되므로(자기 매칭
// 로직에 안 씀) matching/kafkaclient의 디코더는 손대지 않았습니다 — 알지 못하는
// JSON 필드는 그냥 무시됩니다.
type orderEvent struct {
	Type            string `json:"type"` // "NEW" | "CANCEL"
	OrderID         string `json:"orderId"`
	Market          string `json:"market"`
	Side            string `json:"side,omitempty"`
	Price           string `json:"price,omitempty"`
	Quantity        string `json:"quantity,omitempty"`
	AcceptedAt      string `json:"acceptedAt,omitempty"`
	ClientRequestID string `json:"clientRequestId,omitempty"`
	Mode            string `json:"mode,omitempty"`
	CanceledAt      string `json:"canceledAt,omitempty"`
	SourceOrderID   string `json:"sourceOrderId,omitempty"`
}

// PublishNew는 신규 접수된 주문을 발행합니다. Key는 market — 같은 마켓 주문이
// 항상 같은 파티션에 순서대로 쌓이게 합니다. clientRequestID는 요청의
// Idempotency-Key 값을, mode는 X-Order-Mode 헤더 값(PAPER_TRADING/REPLAY)을,
// sourceOrderID는 요청 바디의 sourceOrderId(리플레이 주문만 값이 있음)를 그대로
// 전달받아 메시지에 싣습니다(위 orderEvent 설명 참고).
func (p *OrderProducer) PublishNew(ctx context.Context, o *order.Order, clientRequestID, mode, sourceOrderID string) error {
	return p.publish(ctx, o.Market, orderEvent{
		Type:            "NEW",
		OrderID:         o.OrderID,
		Market:          o.Market,
		Side:            o.Side,
		Price:           o.Price,
		Quantity:        o.Quantity,
		AcceptedAt:      o.AcceptedAt,
		ClientRequestID: clientRequestID,
		Mode:            mode,
		SourceOrderID:   sourceOrderID,
	})
}

// PublishCancel은 취소 이벤트를 발행합니다. 신규 주문과 같은 마켓 파티션에 실어서,
// 매칭 엔진이 같은 마켓 안에서는 신규/취소를 순서대로 처리하게 합니다.
// canceledAt은 호출부(cancelOrderHandler)가 이미 계산해둔 취소 시각을 그대로
// 전달받아 싣습니다 — 기록기가 자기 처리 시각이 아니라 실제 취소 시각을 쓸 수
// 있게 하려는 목적입니다.
func (p *OrderProducer) PublishCancel(ctx context.Context, orderID, market, canceledAt string) error {
	return p.publish(ctx, market, orderEvent{Type: "CANCEL", OrderID: orderID, Market: market, CanceledAt: canceledAt})
}

func (p *OrderProducer) publish(ctx context.Context, key string, event orderEvent) error {
	body, err := json.Marshal(event)
	if err != nil {
		return err
	}
	return p.writer.WriteMessages(ctx, kafka.Message{Key: []byte(key), Value: body})
}

// Close는 프로듀서 연결을 정리합니다.
func (p *OrderProducer) Close() error {
	return p.writer.Close()
}
