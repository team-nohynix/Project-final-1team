package kafka

import (
	"context"
	"fmt"
	"log"

	kafka "github.com/segmentio/kafka-go"

	"recorder/events"
)

// AssignmentReader는 assignments 토픽(FR-11 배정/반납 감사 로그)을 소비합니다.
// orders/executions와 마찬가지로 상태를 지킬 게 없는 컨슈머라 평범한 컨슈머
// 그룹으로 충분하고, 같은 이유로 FetchMessage+명시적 CommitMessages를 씁니다
// (kafka/orders_reader.go의 주석 참고 — ReadMessage의 처리-전-커밋 문제).
type AssignmentReader struct {
	reader *kafka.Reader
}

func NewAssignmentReader(broker, topic, groupID string) *AssignmentReader {
	return &AssignmentReader{reader: kafka.NewReader(kafka.ReaderConfig{
		Brokers: []string{broker},
		Topic:   topic,
		GroupID: groupID,
	})}
}

func (r *AssignmentReader) Run(ctx context.Context, handle func(ctx context.Context, ev events.AssignmentEvent) error) error {
	for {
		msg, err := r.reader.FetchMessage(ctx)
		if err != nil {
			return fmt.Errorf("assignments 토픽 읽기 실패: %w", err)
		}

		ev, err := events.DecodeAssignment(msg.Value)
		if err != nil {
			log.Printf("assignments 메시지 디코딩 실패, 건너뜀: %v", err)
			if err := r.reader.CommitMessages(ctx, msg); err != nil {
				log.Printf("assignments 오프셋 커밋 실패: %v", err)
			}
			continue
		}

		if err := handle(ctx, ev); err != nil {
			return fmt.Errorf("assignments 이벤트 처리 실패 (market=%s): %w", ev.Market, err)
		}

		if err := r.reader.CommitMessages(ctx, msg); err != nil {
			return fmt.Errorf("assignments 오프셋 커밋 실패 (market=%s): %w", ev.Market, err)
		}
	}
}

func (r *AssignmentReader) Close() error {
	return r.reader.Close()
}
