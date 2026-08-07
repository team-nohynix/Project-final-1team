// Package kafkaclient는 매칭 엔진의 Kafka 입출력을 담당합니다 — orders 토픽 소비,
// executions 토픽 발행. 메시지 wire 포맷은 orderapi/kafkaclient.go와 같은 이유로
// 독립적으로 다시 선언합니다(모듈 간 타입 비공유 원칙).
package kafkaclient

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	kafka "github.com/segmentio/kafka-go"
	"github.com/shopspring/decimal"

	"matching/engine"
	"matching/orderbook"
)

// orderMessage는 orderapi/kafkaclient.go의 orderEvent와 같은 모양입니다. 이 규격은
// 아직 잠정입니다(FR-22 Kafka 메시지 규격 문서가 없음, orderapi와 동일한 상황).
type orderMessage struct {
	Type     string `json:"type"` // "NEW" | "CANCEL"
	OrderID  string `json:"orderId"`
	Market   string `json:"market"`
	Side     string `json:"side,omitempty"`
	Price    string `json:"price,omitempty"`
	Quantity string `json:"quantity,omitempty"`
}

// MarketLifecycle은 GroupConsumer가 파티션(=마켓)을 인수/처리/반납할 때 호출하는
// 콜백입니다. matching/main.go가 이 인터페이스를 구현해 마켓별 Engine의 생명주기를
// 관리합니다(map[market]*engine.Engine 등) — GroupConsumer 자신은 Engine을 모릅니다.
type MarketLifecycle interface {
	// Acquire는 이 마켓을 새로 담당하게 됐을 때 호출됩니다. 반환하는 offset부터
	// 컨슘을 시작해야 합니다(FR-08과 동일한 Recover() 결과 — Kafka의 커밋 오프셋은
	// 신뢰하지 않습니다).
	Acquire(ctx context.Context, market string) (resumeFromOffset int64, err error)
	// Apply는 이 마켓의 파티션에서 읽은 이벤트 하나를 처리합니다.
	Apply(ctx context.Context, market string, ev engine.OrderEvent) error
	// Release는 이 마켓을 다른 인스턴스에 넘겨주기 직전에 호출됩니다 — 구현체는
	// engine.Engine.Handoff를 동기적으로 호출해 상태를 확정 저장해야 합니다.
	Release(ctx context.Context, market string) error
}

// GroupConsumer는 kafka.ConsumerGroup으로 그룹 멤버십/제너레이션(리밸런스)만
// 조정하고, 실제 메시지 읽기는 그룹 밖에서 파티션을 직접 구독하는 kafka.Reader로
// 합니다(Partition/GroupID는 서로 배타적이라 GroupID를 비워야 합니다) — Kafka의
// 컨슘 오프셋 커밋 메커니즘은 아예 쓰지 않고, 항상 MarketLifecycle.Acquire가
// 돌려주는 우리 자체 Recover() 결과에서 SetOffset합니다. 이 조합(그룹은 멤버십용,
// 실제 읽기는 그룹 밖)은 spike-consumergroup으로 사전에 실제 Kafka를 대상으로
// 검증했습니다 — 조인/강제종료로 인한 리밸런스 양쪽 모두 (파티션, offset) 중복
// 처리 0건.
type GroupConsumer struct {
	cg      *kafka.ConsumerGroup
	broker  string
	topic   string
	markets []string // 인덱스 = 파티션 번호, orderapi의 marketPartitioner와 동일한 순서
	life    MarketLifecycle
}

// NewGroupConsumer는 GroupConsumer를 만듭니다. groupID는 이 매칭 엔진 배포 전체가
// 공유하는 고정값이어야 합니다(인스턴스별로 다르면 각자 자기 혼자만의 그룹에
// 들어가서 재분배 자체가 일어나지 않습니다) — balancer는 matching/rebalance의
// LoadAwareBalancer를 넘겨줍니다.
func NewGroupConsumer(broker, groupID, topic string, balancer kafka.GroupBalancer, markets []string, life MarketLifecycle) (*GroupConsumer, error) {
	cg, err := kafka.NewConsumerGroup(kafka.ConsumerGroupConfig{
		ID:             groupID,
		Brokers:        []string{broker},
		Topics:         []string{topic},
		GroupBalancers: []kafka.GroupBalancer{balancer},
	})
	if err != nil {
		return nil, fmt.Errorf("컨슈머 그룹 생성 실패: %w", err)
	}
	return &GroupConsumer{cg: cg, broker: broker, topic: topic, markets: markets, life: life}, nil
}

// Run은 제너레이션이 바뀔 때마다(=리밸런스) 이번 제너레이션에서 배정받은 파티션마다
// 소비 고루틴을 하나씩 gen.Start로 띄웁니다. ctx가 취소되면 반환합니다.
func (c *GroupConsumer) Run(ctx context.Context) error {
	for {
		gen, err := c.cg.Next(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("다음 제너레이션 획득 실패: %w", err)
		}

		assignments := gen.Assignments[c.topic]
		log.Printf("[rebalance] 새 제너레이션(id=%d) 시작, 이 인스턴스 배정 파티션 %d개", gen.ID, len(assignments))

		for _, assignment := range assignments {
			partition := assignment.ID
			market := c.markets[partition]
			gen.Start(func(genCtx context.Context) {
				c.consumePartition(genCtx, market, partition)
			})
		}
	}
}

func (c *GroupConsumer) Close() error {
	return c.cg.Close()
}

// consumePartition은 파티션 하나(=마켓 하나)를 이 제너레이션 동안 담당합니다.
// genCtx가 끝나면(=다음 리밸런스로 이 파티션을 내줘야 함) 읽기를 멈추고 Release로
// 상태를 확정 저장한 뒤에야 반환합니다 — gen.Start의 계약(모든 함수가 반환해야
// 다음 제너레이션으로 넘어감)이 바로 "새 담당자가 인수하기 전에 이전 담당자가
// 완전히 멈추고 확정 저장까지 끝낸다"는 보장을 대신 해줍니다.
func (c *GroupConsumer) consumePartition(genCtx context.Context, market string, partition int) {
	resumeFrom, err := c.life.Acquire(genCtx, market)
	if err != nil {
		log.Printf("[%s] 파티션 %d 인수 실패: %v", market, partition, err)
		return
	}
	log.Printf("[%s] 파티션 %d 담당 시작 (offset %d부터)", market, partition, resumeFrom)

	reader := kafka.NewReader(kafka.ReaderConfig{
		Brokers:   []string{c.broker},
		Topic:     c.topic,
		Partition: partition,
		GroupID:   "", // 컨슈머 그룹은 멤버십 조정에만 쓰고 실제 읽기는 그룹 밖에서 함
	})
	defer reader.Close()

	if err := reader.SetOffset(resumeFrom); err != nil {
		log.Printf("[%s] 파티션 %d SetOffset(%d) 실패: %v", market, partition, resumeFrom, err)
	} else {
		c.readLoop(genCtx, reader, market)
	}

	// genCtx가 끝났거나(정상적인 리밸런스) 읽기 루프가 에러로 빠져나온 경우 모두,
	// 이 마켓을 더 이상 담당하지 않게 되는 것이므로 확정 저장은 항상 시도합니다.
	// Handoff는 배경 컨텍스트로 호출합니다 — genCtx는 이미(또는 곧) 취소되므로
	// 저장 자체가 취소되면 안 됩니다.
	if err := c.life.Release(context.Background(), market); err != nil {
		log.Printf("[%s] 파티션 %d 반납(핸드오프 저장) 실패: %v", market, partition, err)
	} else {
		log.Printf("[%s] 파티션 %d 담당 종료", market, partition)
	}
}

func (c *GroupConsumer) readLoop(ctx context.Context, reader *kafka.Reader, market string) {
	for {
		msg, err := reader.ReadMessage(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return // 제너레이션 종료로 인한 정상적인 취소
			}
			log.Printf("[%s] orders 파티션 읽기 실패: %v", market, err)
			return
		}

		var raw orderMessage
		if err := json.Unmarshal(msg.Value, &raw); err != nil {
			log.Printf("[%s] orders 메시지 파싱 실패, 건너뜀: %v", market, err)
			continue
		}

		ev, err := toOrderEvent(raw, msg.Offset)
		if err != nil {
			log.Printf("[%s] orders 메시지 변환 실패, 건너뜀 (orderId=%s): %v", market, raw.OrderID, err)
			continue
		}

		if err := c.life.Apply(ctx, market, ev); err != nil {
			log.Printf("[%s] 이벤트 처리 실패 (orderId=%s): %v", market, ev.OrderID, err)
		}
	}
}

func toOrderEvent(raw orderMessage, offset int64) (engine.OrderEvent, error) {
	ev := engine.OrderEvent{OrderID: raw.OrderID, Market: raw.Market, Offset: offset}

	switch raw.Type {
	case "NEW":
		ev.Type = engine.EventNew
	case "CANCEL":
		ev.Type = engine.EventCancel
		return ev, nil // CANCEL 메시지에는 price/quantity/side가 없음
	default:
		return ev, fmt.Errorf("알 수 없는 이벤트 타입: %q", raw.Type)
	}

	switch raw.Side {
	case "BUY":
		ev.Side = orderbook.Buy
	case "SELL":
		ev.Side = orderbook.Sell
	default:
		return ev, fmt.Errorf("알 수 없는 side: %q", raw.Side)
	}

	price, err := decimal.NewFromString(raw.Price)
	if err != nil {
		return ev, fmt.Errorf("price 파싱 실패: %w", err)
	}
	qty, err := decimal.NewFromString(raw.Quantity)
	if err != nil {
		return ev, fmt.Errorf("quantity 파싱 실패: %w", err)
	}
	ev.Price = price
	ev.Quantity = qty
	return ev, nil
}
