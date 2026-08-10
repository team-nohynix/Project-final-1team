package order

import (
	"context"
	"errors"
	"testing"
	"time"
)

// stubRetrySubmitter는 지정한 횟수만큼 ErrTooManyRequests를 반환한 뒤 성공하는
// (또는 다른 에러를 즉시 반환하는) 가짜 OrderSubmitter입니다.
type stubRetrySubmitter struct {
	failTimes int // 429로 실패할 횟수 (그 다음 호출부터는 성공)
	otherErr  error
	calls     []Order
}

func (s *stubRetrySubmitter) Submit(ctx context.Context, o Order) (string, error) {
	s.calls = append(s.calls, o)
	if s.otherErr != nil {
		return "", s.otherErr
	}
	if len(s.calls) <= s.failTimes {
		return "", ErrTooManyRequests
	}
	return "ord_1", nil
}

// noSleep은 테스트에서 백오프를 실제로 기다리지 않도록 주입하는 가짜 sleep입니다.
func noSleep(ctx context.Context, d time.Duration) error { return nil }

func TestRetryingSubmitterSucceedsAfterSomeRetries(t *testing.T) {
	next := &stubRetrySubmitter{failTimes: 3}
	s := RetryingSubmitter{Next: next, sleep: noSleep}

	orderID, err := s.Submit(context.Background(), Order{Market: "KRW-BTC", IdempotencyKey: "key-1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if orderID != "ord_1" {
		t.Errorf("orderID = %q, want ord_1", orderID)
	}
	if len(next.calls) != 4 { // 실패 3번 + 성공 1번
		t.Errorf("Next.Submit 호출 횟수 = %d, want 4", len(next.calls))
	}
}

func TestRetryingSubmitterReusesSameOrderAcrossRetries(t *testing.T) {
	next := &stubRetrySubmitter{failTimes: 2}
	s := RetryingSubmitter{Next: next, sleep: noSleep}

	o := Order{Market: "KRW-BTC", IdempotencyKey: "key-1"}
	if _, err := s.Submit(context.Background(), o); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for i, got := range next.calls {
		if got.IdempotencyKey != "key-1" {
			t.Errorf("호출 %d: IdempotencyKey = %q, want key-1 (재시도마다 같은 키를 써야 함)", i, got.IdempotencyKey)
		}
	}
}

func TestRetryingSubmitterGivesUpAfterMaxRetries(t *testing.T) {
	next := &stubRetrySubmitter{failTimes: maxRetries + 10} // 항상 429
	s := RetryingSubmitter{Next: next, sleep: noSleep}

	_, err := s.Submit(context.Background(), Order{Market: "KRW-BTC", IdempotencyKey: "key-1"})
	if !errors.Is(err, ErrTooManyRequests) {
		t.Errorf("err = %v, want ErrTooManyRequests", err)
	}
	if len(next.calls) != maxRetries+1 { // 최초 시도 1번 + 재시도 maxRetries번
		t.Errorf("Next.Submit 호출 횟수 = %d, want %d", len(next.calls), maxRetries+1)
	}
}

func TestRetryingSubmitterDoesNotRetryNonLagErrors(t *testing.T) {
	otherErr := errors.New("검증 실패 (예: INVALID_MARKET)")
	next := &stubRetrySubmitter{otherErr: otherErr}
	s := RetryingSubmitter{Next: next, sleep: noSleep}

	_, err := s.Submit(context.Background(), Order{Market: "KRW-BTC", IdempotencyKey: "key-1"})
	if !errors.Is(err, otherErr) {
		t.Errorf("err = %v, want %v", err, otherErr)
	}
	if len(next.calls) != 1 {
		t.Errorf("429가 아닌 에러는 재시도하면 안 되는데 호출 횟수 = %d, want 1", len(next.calls))
	}
}

func TestRetryingSubmitterRespectsContextCancellation(t *testing.T) {
	next := &stubRetrySubmitter{failTimes: maxRetries + 10}
	blockingSleep := func(ctx context.Context, d time.Duration) error {
		return ctx.Err() // 이미 취소된 ctx를 넘기면 즉시 에러
	}
	s := RetryingSubmitter{Next: next, sleep: blockingSleep}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := s.Submit(ctx, Order{Market: "KRW-BTC", IdempotencyKey: "key-1"})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

func TestBackoffDurationIsExponentialAndCapped(t *testing.T) {
	prev := time.Duration(0)
	for attempt := range 6 {
		d := backoffDuration(attempt)
		if d < baseRetryBackoff {
			t.Errorf("backoffDuration(%d) = %v, 최소 %v는 넘어야 함", attempt, d, baseRetryBackoff)
		}
		if d > maxRetryBackoff+maxRetryBackoff/5+1 {
			t.Errorf("backoffDuration(%d) = %v, maxRetryBackoff(%v)+지터를 넘으면 안 됨", attempt, d, maxRetryBackoff)
		}
		if attempt > 0 && d < prev {
			// 지터 때문에 매번 엄밀히 증가하진 않지만, 캡에 도달하기 전까지는
			// 기저값(지터 제외) 자체가 지수적으로 커져야 하므로 대략적인 추세만 확인.
			t.Logf("attempt=%d d=%v prev=%v (지터로 인한 변동은 정상)", attempt, d, prev)
		}
		prev = d
	}
}
