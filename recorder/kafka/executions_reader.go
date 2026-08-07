package kafka

import (
	"context"
	"errors"
	"fmt"
	"log"

	kafka "github.com/segmentio/kafka-go"

	"recorder/events"
)

// ExecutionReader는 executions 토픽을 소비합니다. OrderReader와 별도의
// 컨슈머 그룹 ID를 써서 서로의 파티션 배정에 관여하지 않습니다.
type ExecutionReader struct {
	reader *kafka.Reader
}

func NewExecutionReader(broker, topic, groupID string) *ExecutionReader {
	return &ExecutionReader{reader: kafka.NewReader(kafka.ReaderConfig{
		Brokers: []string{broker},
		Topic:   topic,
		GroupID: groupID,
	})}
}

// Run — orders_reader.go의 Run과 완전히 같은 배칭/커밋 원칙입니다(RDS
// 백프레셔 대응 배칭, 2026-08-07 — CLAUDE.md 참고): batchSize건 또는
// batchFlushInterval 중 먼저 도달하면 handleBatch에 한 번에 넘기고, 성공한
// 뒤에만 그 배치의 오프셋들을 한꺼번에 커밋합니다. kafka.Reader.ReadMessage는
// 처리 전에 즉시 커밋해버려서 DB 쓰기 실패 시 메시지가 재시도 없이 영원히
// 사라지는 문제가 있었음(로컬 검증 중 발견) — FetchMessage+명시적
// CommitMessages를 쓰는 이유는 배칭 전과 동일합니다. 디코딩 실패 시 먼저
// 쌓인 배치를 플러시한 뒤에야 그 메시지의 오프셋을 단독 커밋합니다 — 오프셋
// 커밋 순서가 깨지지 않게 하기 위함입니다(orders_reader.go와 같은 이유).
func (r *ExecutionReader) Run(ctx context.Context, handleBatch func(ctx context.Context, evs []events.ExecutionEvent) error) error {
	var batch []events.ExecutionEvent
	var msgs []kafka.Message

	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		if err := handleBatch(ctx, batch); err != nil {
			return fmt.Errorf("executions 배치 처리 실패 (%d건): %w", len(batch), err)
		}
		if err := r.reader.CommitMessages(ctx, msgs...); err != nil {
			return fmt.Errorf("executions 오프셋 커밋 실패 (%d건): %w", len(batch), err)
		}
		batch = batch[:0]
		msgs = msgs[:0]
		return nil
	}

	for {
		fetchCtx, cancel := context.WithTimeout(ctx, batchFlushInterval)
		msg, err := r.reader.FetchMessage(fetchCtx)
		cancel()
		if err != nil {
			if ctx.Err() != nil {
				return fmt.Errorf("executions 토픽 읽기 중단: %w", ctx.Err())
			}
			if errors.Is(err, context.DeadlineExceeded) {
				if err := flush(); err != nil {
					return err
				}
				continue
			}
			return fmt.Errorf("executions 토픽 읽기 실패: %w", err)
		}

		ev, decodeErr := events.DecodeExecution(msg.Value)
		if decodeErr != nil {
			log.Printf("executions 메시지 디코딩 실패, 건너뜀: %v", decodeErr)
			if err := flush(); err != nil {
				return err
			}
			if err := r.reader.CommitMessages(ctx, msg); err != nil {
				log.Printf("executions 오프셋 커밋 실패: %v", err)
			}
			continue
		}

		batch = append(batch, ev)
		msgs = append(msgs, msg)

		if len(batch) >= batchSize {
			if err := flush(); err != nil {
				return err
			}
		}
	}
}

func (r *ExecutionReader) Close() error {
	return r.reader.Close()
}
