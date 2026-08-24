// Package kafka는 orders/executions/assignments 토픽을 소비하는 얇은
// 리더입니다. 기록기는 matching처럼 마켓별 인메모리 상태를 지키기 위한 리밸런스
// 핸드오프가 필요 없으므로(메시지 하나마다 독립적으로 DB에 쓰기만 함),
// matching/kafkaclient의 ConsumerGroup+커스텀 파티션 밸런서 같은 복잡한 장치
// 없이 평범한 컨슈머 그룹(kafka.Reader{GroupID: ...})으로 충분합니다 — Kafka
// 자체의 파티션 배정과 오프셋 커밋을 그대로 신뢰합니다.
package kafka

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	kafka "github.com/segmentio/kafka-go"

	"recorder/events"
)

// batchSize/batchFlushInterval은 RDS 백프레셔 대응 배칭(2026-08-07, CLAUDE.md의
// "RDS admission control via recorder consumer lag" 참고)의 건수/시간 이중
// 트리거입니다 — matching/engine.Engine.shouldSnapshot(), recorder/archive.Batcher와
// 같은 패턴. OrderReader/ExecutionReader가 각각 자기 배치를 갖습니다.
//
// batchSize는 2026-08-21, DB 배치화(applyFillsBatch/CancelOrdersBatch —
// mysql.go 참고) 이후 200→500으로 상향했습니다: 그 전엔 배치 하나의 DB
// 왕복 횟수가 건수에 비례해서(최대 O(n)) 배치를 키우면 DB 쪽 비용도 같이
// 커졌지만, 지금은 배치 하나의 왕복 횟수가 건수와 무관하게 고정(약 3번)이라
// 배치를 키우는 게 거의 공짜로 처리량을 올리는 레버가 됐습니다.
const (
	batchSize          = 500
	batchFlushInterval = 2 * time.Second

	// readerMinBytes/readerMaxBytes/readerMaxWait/readerQueueCapacity —
	// 2026-08-21, 배치 처리 타이밍이 아니라 그 앞단인 Kafka 저수준 fetch
	// 자체가 별도 병목이라는 게 확인돼 추가(kafka-go@v0.4.51의 reader.go
	// 실제 소스로 확인한 기본값: MinBytes=1, MaxBytes=1e6, MaxWait=10s,
	// QueueCapacity=100). 기본값 MinBytes=1은 브로커가 1바이트만 있어도
	// 즉시 응답해버리고, 기본 QueueCapacity=100은 batchSize(200이었을 때도)
	// 보다 작아서, 앱이 FetchMessage를 batchSize번 부르는 동안 실제로는
	// 여러 번의 개별 브로커 왕복이 끼어드는 구조였습니다 — DB가 유휴 상태인데도
	// 컨슈머 랙이 안 줄던 현상의 원인. 명시적으로 크게 잡아서 kafka-go가
	// 내부적으로 더 큰 단위로 브로커에서 받아오고, 애플리케이션(FetchMessage
	// 반복 호출)은 이미 채워진 내부 버퍼에서 꺼내 쓰게 만듭니다. MaxWait을
	// 기본 10초보다 훨씬 짧은 500ms로 낮춘 이유: MinBytes(1MB)를 못 채우는
	// 저부하 구간(예: 물량이 적은 마켓의 파티션)에서도 최대 500ms 안엔
	// 응답이 오도록 지연 상한을 두기 위함 — 그래도 앱 레벨 batchFlushInterval
	// (2초)보다 훨씬 짧아서 새로운 병목이 되지는 않습니다. QueueCapacity는
	// 새 batchSize(500)보다 넉넉하게 잡았습니다.
	readerMinBytes      = 1e6
	readerMaxBytes      = 10e6
	readerMaxWait       = 500 * time.Millisecond
	readerQueueCapacity = 1000
)

// OrderReader는 orders 토픽을 소비합니다.
type OrderReader struct {
	reader *kafka.Reader
}

// useIAM이 false면(로컬 dev-kafka) 인증 없이 붙고, true면(MSK) AWS_MSK_IAM+TLS로
// 인증합니다(auth.go 참고).
func NewOrderReader(ctx context.Context, broker, topic, groupID string, useIAM bool) (*OrderReader, error) {
	dialer, err := newDialer(ctx, useIAM)
	if err != nil {
		return nil, fmt.Errorf("Kafka SASL 메커니즘 생성 실패: %w", err)
	}
	return &OrderReader{reader: kafka.NewReader(kafka.ReaderConfig{
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

// Run은 메시지를 배치로 모아 handleBatch에 넘깁니다 — 메시지 1건당 DB 왕복
// 1번이던 것을 batchSize건 또는 batchFlushInterval 중 먼저 도달하면 한 번에
// 처리하도록 바꿔, RDS 쪽 왕복/커밋 횟수를 줄입니다(recorder RDS 백프레셔
// 대응). handleBatch가 성공한 뒤에야 그 배치에 속한 메시지들의 오프셋을
// 한꺼번에 커밋합니다 — 배치 처리가 실패하면 그 배치의 어떤 메시지도 커밋되지
// 않으므로, 재시작 후 배치 전체가 그대로 재시도됩니다(기존 단건 처리와 같은
// "DB 반영 성공 후에만 커밋" 원칙을 배치 단위로 확장한 것뿐입니다).
//
// kafka.Reader.ReadMessage는 메시지를 가져오는 즉시(호출부가 처리하기도 전에)
// 오프셋을 커밋해버리는 문제가 있어(FR-09 "Kafka 발행 건수와 DB 저장 건수가
// 일치" 검증 기준을 깨는 실제 버그, 로컬 검증 중 발견) FetchMessage+명시적
// CommitMessages를 쓰는 건 배칭 전과 동일합니다.
//
// 디코딩 실패(메시지 자체가 깨짐)는 재시도해도 똑같이 실패하므로 즉시
// 건너뛰지만, 그 전에 지금까지 쌓인 배치를 먼저 플러시합니다 — 순서가
// 중요합니다: 디코딩 실패한 메시지의 오프셋을 먼저 커밋해버리면, 그보다
// 낮은 오프셋인 아직 미처리 배치가 있을 때 그 오프셋들을 영원히 건너뛰게
// 됩니다(파티션 안에서 오프셋 커밋은 반드시 실제로 처리(또는 의도적으로
// 건너뛰기로 확정)된 순서를 지켜야 함). handleBatch 실패는 커밋하지 않고
// 에러를 반환해 이 리더를 멈춥니다 — main.go가 log.Fatal로 전체 프로세스를
// 재시작시키면, 다음 실행이 마지막으로 커밋된 오프셋부터 다시 읽어 재시도합니다.
func (r *OrderReader) Run(ctx context.Context, handleBatch func(ctx context.Context, evs []events.OrderEvent) error) error {
	var batch []events.OrderEvent
	var msgs []kafka.Message

	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		if err := handleBatch(ctx, batch); err != nil {
			return fmt.Errorf("orders 배치 처리 실패 (%d건): %w", len(batch), err)
		}
		if err := r.reader.CommitMessages(ctx, msgs...); err != nil {
			return fmt.Errorf("orders 오프셋 커밋 실패 (%d건): %w", len(batch), err)
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
				return fmt.Errorf("orders 토픽 읽기 중단: %w", ctx.Err())
			}
			if errors.Is(err, context.DeadlineExceeded) {
				// 배치 시간 트리거 — 그동안 새 메시지가 없었어도 쌓인 게 있으면 플러시.
				if err := flush(); err != nil {
					return err
				}
				continue
			}
			return fmt.Errorf("orders 토픽 읽기 실패: %w", err)
		}

		ev, decodeErr := events.DecodeOrderEvent(msg.Value)
		if decodeErr != nil {
			log.Printf("orders 메시지 디코딩 실패, 건너뜀: %v", decodeErr)
			// 낮은 오프셋의 미처리 배치를 먼저 커밋해야 오프셋 순서가 안 깨집니다.
			if err := flush(); err != nil {
				return err
			}
			if err := r.reader.CommitMessages(ctx, msg); err != nil {
				log.Printf("orders 오프셋 커밋 실패: %v", err)
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

func (r *OrderReader) Close() error {
	return r.reader.Close()
}

// Lag는 이 리더의 현재 컨슈머 랙(가장 최근 읽은 배치의 하이워터마크 - 오프셋,
// 메시지 건수)입니다 — RDS 백프레셔 감시(`backpressure.Watcher`)가 씁니다.
// `kafka.Reader.Lag()`/`ReadLag()`는 컨슈머 그룹 모드에서 각각 -1/에러를
// 반환하지만(공식 문서에 명시), `Stats().Lag`는 그룹 모드 여부와 무관하게
// FetchMessage가 배치를 읽을 때마다 갱신되는 별도 통계값이라 여기선 안전하게
// 씁니다(세그멘토 kafka-go@v0.4.51 소스 확인).
func (r *OrderReader) Lag() int64 {
	return r.reader.Stats().Lag
}
