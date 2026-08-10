package backpressure

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"
)

// flagTTL은 백프레셔 플래그 키의 Redis TTL입니다 — 기록기가 크래시로 플래그를
// 못 끄더라도 이 시간 뒤 자동으로 사라져 orderapi가 다시 신규 주문을 받기
// 시작합니다(자기치유, orderapi/session의 세션 TTL·matching/kafkaclient의
// 컨슈머 그룹 세션 타임아웃과 같은 설계 철학 — fail-open이 fail-closed보다
// 안전하다고 판단: 플래그가 사라져서 주문을 좀 더 받는 것이, 기록기가 죽었는데
// 영원히 주문을 거절하는 것보다 낫습니다). Watcher.CheckInterval보다 충분히
// 길게 잡아서, 검사 주기 사이의 짧은 지연으로 플래그가 깜빡이지 않게 합니다.
const flagTTL = 15 * time.Second

// RedisFlag는 backpressure.FlagSetter를 Redis로 구현합니다. 이 파일은 손으로
// 검증합니다(matching/kafkaclient·snapshotstore와 같은 이유) — 실제 Redis
// 왕복이 핵심이라 단위 테스트로는 의미 있게 못 검증합니다. 키의 존재 자체가
// "활성" 신호입니다(orderapi/session의 활성 키와 같은 관례) — 값 내용은
// 안 봅니다.
type RedisFlag struct {
	Client *redis.Client
	Key    string
}

func (f *RedisFlag) SetActive(ctx context.Context, active bool) error {
	if active {
		return f.Client.Set(ctx, f.Key, "1", flagTTL).Err()
	}
	return f.Client.Del(ctx, f.Key).Err()
}
