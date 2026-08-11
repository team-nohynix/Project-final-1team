package order

import (
	"context"
	"errors"
	"log"
	"math/rand"
	"time"
)

// maxRetries/baseRetryBackoff/maxRetryBackoff는 이 프로젝트 다른 곳의 봇
// 파라미터들처럼 실측 없이 잡은 잠정값입니다 — 실제 부하테스트로 재조정할 걸
// 전제로 합니다.
const (
	maxRetries       = 5
	baseRetryBackoff = 500 * time.Millisecond
	maxRetryBackoff  = 10 * time.Second
)

// RetryingSubmitter는 다른 OrderSubmitter를 감싸서, orderapi가 429(RDS
// 백프레셔, CLAUDE.md의 "RDS admission control via recorder consumer lag"
// 참고)로 거절한 주문만 지수 백오프(+지터)로 재시도합니다. 429가 아닌 다른
// 실패(검증 오류, 네트워크 오류 등)는 즉시 그대로 반환합니다 — 429만 "잠시
// 후 다시 시도하면 성공할 수 있는" 일시적 상태이기 때문입니다.
//
// 재시도가 그 호출을 한 고루틴을 그동안 블로킹합니다(비동기 큐 방식은 일부러
// 안 씀) — trader는 마켓별·봇 종류별로 독립된 고루틴을 돌리므로, 한 고루틴이
// 재시도를 기다리는 동안 그 고루틴만 다음 결정을 못 만들 뿐 다른 마켓/봇은
// 전혀 영향받지 않습니다. 오히려 이게 그 봇 스스로 속도를 줄이는 자연스러운
// 자기조절이 됩니다.
//
// replayengine에는 이 재시도 로직을 일부러 안 넣습니다 — 리플레이는 매번
// 같은 순서/타이밍으로 재현돼야 하는데(FR-18), 재시도의 실시간 대기가 그
// 순서를 실행마다 다르게 만들어서 before/after 비교를 무의미하게 만들기
// 때문입니다(팀 결정).
type RetryingSubmitter struct {
	Next OrderSubmitter

	// sleep은 백오프 대기를 실제로 수행합니다. nil이면 defaultSleep(실제 타이머)을
	// 씁니다 — 테스트에서 즉시 반환하는 가짜로 바꿔서 백오프를 실제로 기다리지
	// 않고 검증할 수 있게 하기 위한 주입 지점입니다.
	sleep func(ctx context.Context, d time.Duration) error
}

// Submit은 Next.Submit을 호출하고, ErrTooManyRequests가 반환되면 최대
// maxRetries번까지 지수 백오프 후 같은 o로 다시 시도합니다. o.IdempotencyKey는
// 생성 시점에 고정돼 있으므로(order.go) 재시도마다 자동으로 같은 키가 나갑니다.
func (s RetryingSubmitter) Submit(ctx context.Context, o Order) (string, error) {
	sleep := s.sleep
	if sleep == nil {
		sleep = defaultSleep
	}

	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		orderID, err := s.Next.Submit(ctx, o)
		if err == nil {
			return orderID, nil
		}
		if !errors.Is(err, ErrTooManyRequests) {
			return "", err
		}
		lastErr = err
		if attempt == maxRetries {
			break
		}

		wait := backoffDuration(attempt)
		log.Printf("[%s] 429 수신, %v 후 재시도 (%d/%d)", o.Market, wait, attempt+1, maxRetries)
		if err := sleep(ctx, wait); err != nil {
			return "", err
		}
	}

	log.Printf("[%s] 재시도 %d회 모두 429로 실패, 주문 포기", o.Market, maxRetries)
	return "", lastErr
}

// backoffDuration은 attempt(0부터 시작) 번째 재시도 전 대기 시간을 지수적으로
// 늘리되 maxRetryBackoff로 캡핑하고, 0~20% 지터를 더합니다. 지터가 필요한
// 이유: 백프레셔가 걸리면 여러 마켓의 여러 봇이 거의 동시에 429를 받으므로,
// 지터 없이 전부 똑같은 시점에 재시도하면 그 자체로 다시 요청 스파이크를
// 만들 수 있습니다.
func backoffDuration(attempt int) time.Duration {
	d := baseRetryBackoff * time.Duration(1<<uint(attempt))
	if d > maxRetryBackoff {
		d = maxRetryBackoff
	}
	jitter := time.Duration(rand.Int63n(int64(d)/5 + 1))
	return d + jitter
}

func defaultSleep(ctx context.Context, d time.Duration) error {
	select {
	case <-time.After(d):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
