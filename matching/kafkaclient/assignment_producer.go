package kafkaclient

import (
	"context"
	"encoding/json"
	"time"

	kafka "github.com/segmentio/kafka-go"
)

// assignmentMessage는 assignments 토픽에 싣는 메시지 모양입니다 — FR-11 재분배로
// 마켓을 인수/반납할 때마다 발행합니다. "기록기"가 이걸 구독해서
// docs/erd.md의 MATCHING_ENGINE_ASSIGNMENT를 채웁니다. orderapi/matching의
// 다른 메시지들과 마찬가지로 FR-22 규격 문서가 없어 잠정입니다.
type assignmentMessage struct {
	Type             string `json:"type"` // "ASSIGNED" | "RELEASED"
	Market           string `json:"market"`
	EngineInstanceID string `json:"engineInstanceId"`
	At               string `json:"at"`
}

// AssignmentProducer는 배정/반납 이벤트를 assignments 토픽에 발행합니다. 이건
// 감사/기록 목적일 뿐 매칭 엔진 자신의 정합성에는 영향이 없으므로(진짜 상태는
// Redis 스냅샷+Handoff가 이미 보장함), 발행 실패는 로그만 남기고 Acquire/Release
// 자체를 막지 않습니다 — 호출부(main.go의 marketRegistry) 참고.
type AssignmentProducer struct {
	writer *kafka.Writer
}

func NewAssignmentProducer(broker, topic string) *AssignmentProducer {
	return &AssignmentProducer{
		writer: &kafka.Writer{
			Addr:                   kafka.TCP(broker),
			Topic:                  topic,
			Balancer:               &kafka.Hash{},
			BatchTimeout:           10 * time.Millisecond,
			AllowAutoTopicCreation: true,
		},
	}
}

func (p *AssignmentProducer) PublishAssigned(ctx context.Context, market, instanceID string) error {
	return p.publish(ctx, market, "ASSIGNED", instanceID)
}

func (p *AssignmentProducer) PublishReleased(ctx context.Context, market, instanceID string) error {
	return p.publish(ctx, market, "RELEASED", instanceID)
}

func (p *AssignmentProducer) publish(ctx context.Context, market, eventType, instanceID string) error {
	body, err := json.Marshal(assignmentMessage{
		Type:             eventType,
		Market:           market,
		EngineInstanceID: instanceID,
		At:               time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
	})
	if err != nil {
		return err
	}
	return p.writer.WriteMessages(ctx, kafka.Message{Key: []byte(market), Value: body})
}

func (p *AssignmentProducer) Close() error {
	return p.writer.Close()
}
