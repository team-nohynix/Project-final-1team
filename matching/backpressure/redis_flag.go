package backpressure

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"
)

// flagTTL은 recorder/backpressure.RedisFlag와 같은 이유로 15초입니다 —
// Watcher.CheckInterval보다 충분히 길게 잡아 검사 주기 사이의 짧은 지연으로
// 깜빡이지 않게 하고, 인스턴스가 크래시해도 자동으로 사라지게 합니다.
const flagTTL = 15 * time.Second

// RedisFlag는 backpressure.FlagSetter를 Redis로 구현합니다. recorder용
// RedisFlag와의 핵심 차이: **비활성일 때 키를 명시적으로 지우지 않습니다**
// (SetActive(ctx, false)는 아무 것도 안 함, nil 반환). matching은 FR-11로
// 여러 인스턴스가 동시에 20개 마켓을 나눠 담당하는데, 이 Watcher/Flag는
// 인스턴스마다 하나씩 떠서 "이 인스턴스가 지금 담당 중인 파티션들의 랙
// 합계"만 보고 같은 Redis 키를 공유해서 씁니다(키 하나 = "matching 전체에
// 뭔가 밀리고 있다"는 전역 신호). recorder처럼 비활성일 때 Del을 호출하면,
// 회복된 인스턴스 A가 자기 랙은 괜찮아졌다고 키를 지웠는데 아직 과부하인
// 인스턴스 B가 방금 세운 플래그를 지워버리는 레이스가 생깁니다 — 그래서
// "켜는 쪽"만 명시적으로 하고, "끄는 쪽"은 아무도 갱신을 안 해서 TTL이
// 만료되는 것으로만 처리합니다(모든 인스턴스가 동시에 비활성이어야 자연히
// 갱신이 멈추고 15초 뒤 사라짐 — self-healing, orderapi/session의 TTL
// 철학과 동일).
type RedisFlag struct {
	Client *redis.Client
	Key    string
}

func (f *RedisFlag) SetActive(ctx context.Context, active bool) error {
	if !active {
		return nil
	}
	return f.Client.Set(ctx, f.Key, "1", flagTTL).Err()
}
