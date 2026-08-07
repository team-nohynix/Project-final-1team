package rebalance

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
	kafka "github.com/segmentio/kafka-go"
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
	markets []string // 인덱스 = 파티션 번호(orderapi/kafkaclient의 marketPartitioner와 동일한 순서)
}

func NewLoadTracker(redisClient *redis.Client, broker, topic string, markets []string) *LoadTracker {
	return &LoadTracker{redis: redisClient, broker: broker, topic: topic, markets: markets}
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
func (t *LoadTracker) Loads(ctx context.Context) ([]Load, error) {
	now := time.Now()
	loads := make([]Load, 0, len(t.markets))

	for partition, market := range t.markets {
		offset, err := t.readLastOffset(ctx, partition)
		if err != nil {
			return nil, fmt.Errorf("파티션 %d(마켓 %s) 오프셋 조회 실패: %w", partition, market, err)
		}

		load := 0.0
		prev, ok, err := t.loadReading(ctx, market)
		if err != nil {
			return nil, fmt.Errorf("마켓 %s 이전 측정값 조회 실패: %w", market, err)
		}
		if ok {
			elapsed := now.Sub(time.Unix(0, prev.AtUnix)).Seconds()
			if elapsed > 0 && offset > prev.Offset {
				load = float64(offset-prev.Offset) / elapsed
			}
		}
		loads = append(loads, Load{Market: market, Value: load})

		if err := t.saveReading(ctx, market, reading{Offset: offset, AtUnix: now.UnixNano()}); err != nil {
			return nil, fmt.Errorf("마켓 %s 측정값 저장 실패: %w", market, err)
		}
	}
	return loads, nil
}

func (t *LoadTracker) readLastOffset(ctx context.Context, partition int) (int64, error) {
	conn, err := kafka.DialLeader(ctx, "tcp", t.broker, t.topic, partition)
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
