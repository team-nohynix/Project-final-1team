package rebalance

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
	kafka "github.com/segmentio/kafka-go"

	"matching/kafkaclient"
)

// LoadTracker는 파티션별 최신 오프셋을 브로커에서 직접 읽어(인스턴스 쪽에서 뭔가
// 리포트할 필요 없음 — 브로커가 진실의 원천) 이전 측정값과의 차이로 "최근 처리량"을
// 추정합니다. 이전 측정값은 Redis에 저장합니다 — Kafka 그룹의 리더는 리밸런스마다
// 바뀔 수 있어서, 이 상태가 특정 프로세스의 메모리에만 있으면 리더가 바뀌는 순간
// 측정 이력이 끊깁니다.
type LoadTracker struct {
	redis   *redis.Client
	broker  string
	topic   string
	markets []string      // 인덱스 = 파티션 번호(orderapi/kafkaclient의 marketPartitioner와 동일한 순서)
	dialer  *kafka.Dialer // nil이면 인증 없음(로컬), 아니면 AWS_MSK_IAM+TLS(MSK) — kafkaclient/auth.go 참고
}

// useIAM이 false면(로컬 dev-kafka) 인증 없이 붙고, true면(MSK) AWS_MSK_IAM+TLS로
// 인증합니다. readLastOffset이 kafka.DialLeader로 브로커에 직접 붙는 자리라
// GroupConsumer와는 별도로 dialer가 필요합니다.
func NewLoadTracker(ctx context.Context, redisClient *redis.Client, broker, topic string, markets []string, useIAM bool) (*LoadTracker, error) {
	dialer, err := kafkaclient.NewDialer(ctx, useIAM)
	if err != nil {
		return nil, fmt.Errorf("Kafka SASL 메커니즘 생성 실패: %w", err)
	}
	return &LoadTracker{redis: redisClient, broker: broker, topic: topic, markets: markets, dialer: dialer}, nil
}

type reading struct {
	Offset int64 `json:"offset"`
	AtUnix int64 `json:"atUnix"` // UnixNano
}

func readingKey(market string) string {
	return "rebalance:load:" + market
}

// Loads는 마켓별 최근 처리량 추정치를 반환합니다. 처음 측정하는 마켓(이전 기록이
// 없음)은 0으로 보고합니다 — 콜드 스타트를 "가볍다"고 가정하는 게, 알지도 못하는
// 값으로 잘못 추정해 배정을 왜곡하는 것보다 안전한 기본값입니다.
//
// 마켓 20개를 병렬로 측정합니다 — 원래는 순차 for문이었는데, readLastOffset이
// 마켓마다 매번 새로 브로커에 다이얼합니다(AWS_MSK_IAM이면 TLS+SASL 핸드셰이크까지
// 매번 새로). 이게 AssignGroups(리더가 리밸런스 중 동기 호출) 안에서 20번 순차로
// 일어나면 실측 약 2초가 걸렸고, 이 블로킹이 그룹 리밸런스 자체를 지연시켜
// 코디네이터가 리밸런스가 멈췄다고 보고 다시 리밸런스를 트리거 — 그 안에서
// Loads()가 다시 20번 순차 다이얼을 반복하는 자기유발 루프가 됐습니다(실측:
// 제너레이션이 몇 초마다 계속 바뀌고 모든 마켓의 Acquire가 "generation has
// ended"로 실패). 병렬화하면 전체 소요시간이 "가장 느린 다이얼 1개" 수준으로
// 줄어 이 타이밍 압박이 사라집니다.
func (t *LoadTracker) Loads(ctx context.Context) ([]Load, error) {
	now := time.Now()
	loads := make([]Load, len(t.markets))

	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		firstErr error
	)
	for partition, market := range t.markets {
		wg.Add(1)
		go func(partition int, market string) {
			defer wg.Done()

			load, err := t.marketLoad(ctx, partition, market, now)
			if err != nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				mu.Unlock()
				return
			}
			loads[partition] = load
		}(partition, market)
	}
	wg.Wait()

	if firstErr != nil {
		return nil, firstErr
	}
	return loads, nil
}

// marketLoad는 마켓 하나의 최근 처리량을 측정합니다 — Loads()가 마켓마다
// 고루틴으로 병렬 호출합니다.
func (t *LoadTracker) marketLoad(ctx context.Context, partition int, market string, now time.Time) (Load, error) {
	offset, err := t.readLastOffset(ctx, partition)
	if err != nil {
		return Load{}, fmt.Errorf("파티션 %d(마켓 %s) 오프셋 조회 실패: %w", partition, market, err)
	}

	load := 0.0
	prev, ok, err := t.loadReading(ctx, market)
	if err != nil {
		return Load{}, fmt.Errorf("마켓 %s 이전 측정값 조회 실패: %w", market, err)
	}
	if ok {
		elapsed := now.Sub(time.Unix(0, prev.AtUnix)).Seconds()
		if elapsed > 0 && offset > prev.Offset {
			load = float64(offset-prev.Offset) / elapsed
		}
	}

	if err := t.saveReading(ctx, market, reading{Offset: offset, AtUnix: now.UnixNano()}); err != nil {
		return Load{}, fmt.Errorf("마켓 %s 측정값 저장 실패: %w", market, err)
	}
	return Load{Market: market, Value: load}, nil
}

func (t *LoadTracker) readLastOffset(ctx context.Context, partition int) (int64, error) {
	// t.dialer가 nil이면(로컬, 인증 없음) 패키지 레벨 kafka.DialLeader(내부적으로
	// kafka.DefaultDialer 사용)를 그대로 씁니다 — nil *kafka.Dialer에 메서드를
	// 직접 호출하면 필드 접근에서 패닉이 나므로, 인증이 있을 때만 t.dialer.DialLeader를 씁니다.
	var (
		conn *kafka.Conn
		err  error
	)
	if t.dialer != nil {
		conn, err = t.dialer.DialLeader(ctx, "tcp", t.broker, t.topic, partition)
	} else {
		conn, err = kafka.DialLeader(ctx, "tcp", t.broker, t.topic, partition)
	}
	if err != nil {
		return 0, err
	}
	defer conn.Close()
	return conn.ReadLastOffset()
}

func (t *LoadTracker) loadReading(ctx context.Context, market string) (reading, bool, error) {
	body, err := t.redis.Get(ctx, readingKey(market)).Bytes()
	if err == redis.Nil {
		return reading{}, false, nil
	}
	if err != nil {
		return reading{}, false, err
	}
	var r reading
	if err := json.Unmarshal(body, &r); err != nil {
		return reading{}, false, err
	}
	return r, true, nil
}

func (t *LoadTracker) saveReading(ctx context.Context, market string, r reading) error {
	body, err := json.Marshal(r)
	if err != nil {
		return err
	}
	return t.redis.Set(ctx, readingKey(market), body, 0).Err()
}
