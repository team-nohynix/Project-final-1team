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

// useIAM이 false면(로컬 dev-kafka) 인증 없이 붙고, true면(MSK) AWS_MSK_IAM+TLS로
// 인증합니다(auth.go 참고).
func NewExecutionReader(ctx context.Context, broker, topic, groupID string, useIAM bool) (*ExecutionReader, error) {
	dialer, err := newDialer(ctx, useIAM)
	if err != nil {
		return nil, fmt.Errorf("Kafka SASL 메커니즘 생성 실패: %w", err)
	}
	return &ExecutionReader{reader: kafka.NewReader(kafka.ReaderConfig{
		Brokers:       []string{broker},
		Topic:         topic,
		GroupID:       groupID,
		Dialer:        dialer,
		MinBytes:      readerMinBytes,
		MaxBytes:      readerMaxBytes,
		MaxWait:       readerMaxWait,
		QueueCapacity: readerQueueCapacity,
	})}, nil
}

// Run — orders_reader.go의 Run과 완전히 같은 배칭/커밋/파이프라인 원칙입니다
// (RDS 백프레셔 대응 배칭, 2026-08-07; 파이프라인화, 2026-08-21 — 둘 다
// CLAUDE.md 참고): batchSize건 또는 batchFlushInterval 중 먼저 도달하면
// handleBatch에 한 번에 넘기되, 그 flush를 fetch 루프 안에서 동기 실행하는
// 대신 별도 writer 고루틴에 채널로 넘겨서 다음 배치 수집과 겹치게 합니다.
// 성공한 배치만 그 오프셋들을 한꺼번에 커밋합니다. kafka.Reader.ReadMessage는
// 처리 전에 즉시 커밋해버려서 DB 쓰기 실패 시 메시지가 재시도 없이 영원히
// 사라지는 문제가 있었음(로컬 검증 중 발견) — FetchMessage+명시적
// CommitMessages를 쓰는 이유는 배칭 전과 동일합니다. 디코딩 실패 시 먼저
// 쌓인 배치를 큐에 넣은 뒤에야 그 메시지를 "단독 커밋" 항목으로 같은
// 채널에 넣습니다 — writer가 도착 순서대로 처리하므로 오프셋 커밋 순서가
// 깨지지 않습니다(orders_reader.go와 같은 이유, 자세한 설명은 그쪽 주석 참고).
func (r *ExecutionReader) Run(ctx context.Context, handleBatch func(ctx context.Context, evs []events.ExecutionEvent) error) error {
	type writeItem struct {
		evs  []events.ExecutionEvent
		msgs []kafka.Message
		skip bool
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	items := make(chan writeItem, pipelineDepth)
	writerDone := make(chan struct{})
	var writerErr error

	go func() {
		defer close(writerDone)
		for item := range items {
			if !item.skip {
				if err := handleBatch(ctx, item.evs); err != nil {
					writerErr = fmt.Errorf("executions 배치 처리 실패 (%d건): %w", len(item.evs), err)
					cancel()
					return
				}
			}
			if err := r.reader.CommitMessages(ctx, item.msgs...); err != nil {
				writerErr = fmt.Errorf("executions 오프셋 커밋 실패 (%d건): %w", len(item.msgs), err)
				cancel()
				return
			}
		}
	}()

	var batch []events.ExecutionEvent
	var msgs []kafka.Message

	enqueue := func() bool {
		if len(batch) == 0 {
			return true
		}
		select {
		case items <- writeItem{evs: batch, msgs: msgs}:
			batch = nil
			msgs = nil
			return true
		case <-runCtx.Done():
			return false
		}
	}

	var fetchErr error
loop:
	for {
		fetchCtx, fcancel := context.WithTimeout(runCtx, batchFlushInterval)
		msg, err := r.reader.FetchMessage(fetchCtx)
		fcancel()
		if err != nil {
			if runCtx.Err() != nil {
				break loop
			}
			if errors.Is(err, context.DeadlineExceeded) {
				if !enqueue() {
					break loop
				}
				continue
			}
			fetchErr = fmt.Errorf("executions 토픽 읽기 실패: %w", err)
			break loop
		}

		ev, decodeErr := events.DecodeExecution(msg.Value)
		if decodeErr != nil {
			log.Printf("executions 메시지 디코딩 실패, 건너뜀: %v", decodeErr)
			if !enqueue() {
				break loop
			}
			select {
			case items <- writeItem{msgs: []kafka.Message{msg}, skip: true}:
			case <-runCtx.Done():
				break loop
			}
			continue
		}

		batch = append(batch, ev)
		msgs = append(msgs, msg)

		if len(batch) >= batchSize {
			if !enqueue() {
				break loop
			}
		}
	}

	close(items)
	<-writerDone

	if fetchErr != nil {
		return fetchErr
	}
	if writerErr != nil {
		return writerErr
	}
	if ctx.Err() != nil {
		return fmt.Errorf("executions 토픽 읽기 중단: %w", ctx.Err())
	}
	return nil
}

func (r *ExecutionReader) Close() error {
	return r.reader.Close()
}

// Lag — orders_reader.go의 Lag()와 같은 이유·같은 방식입니다.
func (r *ExecutionReader) Lag() int64 {
	return r.reader.Stats().Lag
}
