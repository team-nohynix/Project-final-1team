package backpressure

import (
	"context"
	"errors"
	"testing"
)

type fakeChecker struct {
	active bool
	err    error
}

func (f *fakeChecker) Active(ctx context.Context) (bool, error) {
	return f.active, f.err
}

func TestMultiCheckerActiveIfAnyActive(t *testing.T) {
	m := MultiChecker{&fakeChecker{active: false}, &fakeChecker{active: true}}
	active, err := m.Active(context.Background())
	if err != nil || !active {
		t.Errorf("Active() = (%v, %v), want (true, nil)", active, err)
	}
}

func TestMultiCheckerInactiveIfAllInactive(t *testing.T) {
	m := MultiChecker{&fakeChecker{active: false}, &fakeChecker{active: false}}
	active, err := m.Active(context.Background())
	if err != nil || active {
		t.Errorf("Active() = (%v, %v), want (false, nil)", active, err)
	}
}

// TestMultiCheckerActiveWinsOverOtherError — 하나가 에러라도 다른 하나가
// active면 그 결론을 뒤집지 않고 즉시 true를 반환해야 합니다.
func TestMultiCheckerActiveWinsOverOtherError(t *testing.T) {
	wantErr := errors.New("redis 연결 실패")
	m := MultiChecker{&fakeChecker{err: wantErr}, &fakeChecker{active: true}}
	active, err := m.Active(context.Background())
	if err != nil || !active {
		t.Errorf("Active() = (%v, %v), want (true, nil)", active, err)
	}
}

// TestMultiCheckerReturnsFirstErrorWhenNoneActive — 전부 비활성/에러인
// 경우, fail-open을 위해 첫 에러를 그대로 돌려줘야 합니다(호출부가 로그하고
// 계속 진행).
func TestMultiCheckerReturnsFirstErrorWhenNoneActive(t *testing.T) {
	wantErr := errors.New("redis 연결 실패")
	m := MultiChecker{&fakeChecker{err: wantErr}, &fakeChecker{active: false}}
	active, err := m.Active(context.Background())
	if active {
		t.Errorf("Active() active = true, want false")
	}
	if !errors.Is(err, wantErr) {
		t.Errorf("Active() err = %v, want %v", err, wantErr)
	}
}

func TestMultiCheckerEmptyIsInactive(t *testing.T) {
	m := MultiChecker{}
	active, err := m.Active(context.Background())
	if err != nil || active {
		t.Errorf("Active() = (%v, %v), want (false, nil)", active, err)
	}
}
