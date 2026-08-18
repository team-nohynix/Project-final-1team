package store

import (
	"errors"
	"testing"

	"github.com/go-sql-driver/mysql"
)

func TestIsRetryableMySQLErrorDeadlock(t *testing.T) {
	err := &mysql.MySQLError{Number: 1213, Message: "Deadlock found"}
	if !isRetryableMySQLError(err) {
		t.Error("데드락(1213)은 재시도 가능해야 하는데 아님")
	}
}

func TestIsRetryableMySQLErrorLockWaitTimeout(t *testing.T) {
	err := &mysql.MySQLError{Number: 1205, Message: "Lock wait timeout exceeded"}
	if !isRetryableMySQLError(err) {
		t.Error("락 대기 타임아웃(1205)은 재시도 가능해야 하는데 아님")
	}
}

func TestIsRetryableMySQLErrorOtherMySQLError(t *testing.T) {
	err := &mysql.MySQLError{Number: 1062, Message: "Duplicate entry"}
	if isRetryableMySQLError(err) {
		t.Error("중복 키(1062)는 재시도 대상이 아닌데 재시도 가능하다고 판단함")
	}
}

func TestIsRetryableMySQLErrorNonMySQLError(t *testing.T) {
	if isRetryableMySQLError(errors.New("아무 에러")) {
		t.Error("MySQL 에러가 아닌데 재시도 가능하다고 판단함")
	}
}

func TestIsRetryableMySQLErrorWrapped(t *testing.T) {
	inner := &mysql.MySQLError{Number: 1213, Message: "Deadlock found"}
	wrapped := errors.Join(errors.New("주문 체결 반영 실패"), inner)
	if !isRetryableMySQLError(wrapped) {
		t.Error("fmt.Errorf(%w)로 감싼 데드락 에러도 errors.As로 찾아내야 하는데 못 찾음")
	}
}

func TestWithRetryOnDeadlockSucceedsAfterSomeRetries(t *testing.T) {
	calls := 0
	err := withRetryOnDeadlockForTest(t, func() error {
		calls++
		if calls <= 3 {
			return &mysql.MySQLError{Number: 1213, Message: "Deadlock found"}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls != 4 {
		t.Errorf("호출 횟수 = %d, want 4 (실패 3번 + 성공 1번)", calls)
	}
}

func TestWithRetryOnDeadlockGivesUpAfterMaxRetries(t *testing.T) {
	calls := 0
	deadlockErr := &mysql.MySQLError{Number: 1213, Message: "Deadlock found"}
	err := withRetryOnDeadlockForTest(t, func() error {
		calls++
		return deadlockErr
	})
	if !errors.Is(err, deadlockErr) && err.Error() != deadlockErr.Error() {
		t.Errorf("err = %v, want %v", err, deadlockErr)
	}
	if calls != maxDBRetries+1 {
		t.Errorf("호출 횟수 = %d, want %d (최초 시도 + 재시도 %d번)", calls, maxDBRetries+1, maxDBRetries)
	}
}

func TestWithRetryOnDeadlockDoesNotRetryNonDeadlockErrors(t *testing.T) {
	calls := 0
	otherErr := errors.New("스키마 문제 등 재시도해도 안 될 에러")
	err := withRetryOnDeadlockForTest(t, func() error {
		calls++
		return otherErr
	})
	if !errors.Is(err, otherErr) {
		t.Errorf("err = %v, want %v", err, otherErr)
	}
	if calls != 1 {
		t.Errorf("데드락이 아닌 에러는 재시도하면 안 되는데 호출 횟수 = %d, want 1", calls)
	}
}

// withRetryOnDeadlockForTest는 withRetryOnDeadlock을 그대로 호출하는 얇은
// 래퍼입니다 — baseDBRetryWait(50ms)가 작아서 최악의 경우(재시도 5번 다 소진)도
// 수 초 안에 끝나므로 실제 대기를 그대로 둡니다. t.Helper()는 실패 시 스택
// 트레이스가 이 래퍼가 아니라 호출부를 가리키게 합니다.
func withRetryOnDeadlockForTest(t *testing.T, fn func() error) error {
	t.Helper()
	return withRetryOnDeadlock(fn)
}
