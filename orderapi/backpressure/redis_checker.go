package backpressure

import (
	"context"
	"errors"

	"github.com/redis/go-redis/v9"
)

// RedisChecker는 Checker를 Redis로 구현합니다. 키가 존재하면(만료 전이면)
// 활성, 없으면(만료됐거나 recorder/backpressure.Watcher가 아직 한 번도 켠
// 적 없으면) 비활성입니다 — 키 값 내용은 안 보고 존재 여부만 봅니다
// (recorder/backpressure.RedisFlag가 쓰는 것과 같은 관례). 이 파일은 손으로
// 검증합니다(session.RedisStore와 같은 이유) — 실제 Redis 왕복이 핵심이라
// 단위 테스트로는 의미 있게 못 검증합니다.
type RedisChecker struct {
	Client *redis.Client
	Key    string
}

func (c *RedisChecker) Active(ctx context.Context) (bool, error) {
	err := c.Client.Get(ctx, c.Key).Err()
	if errors.Is(err, redis.Nil) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}
