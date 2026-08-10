package order

import (
	"strings"
	"testing"
	"time"
)

func TestStoreNewOrderIDFormatAndUniqueness(t *testing.T) {
	s := NewStore()
	now := time.Date(2026, 7, 31, 9, 0, 0, 0, time.UTC)

	id1 := s.NewOrderID(now)
	id2 := s.NewOrderID(now)

	if !strings.HasPrefix(id1, "ord_20260731_") {
		t.Errorf("id1 = %q, want ord_20260731_ 접두사", id1)
	}
	if id1 == id2 {
		t.Errorf("연속 발급한 두 주문 번호가 같으면 안 되는데: %q, %q", id1, id2)
	}
}

func TestStoreSaveAndGet(t *testing.T) {
	s := NewStore()
	o := &Order{OrderID: "ord_test_1", Market: "KRW-BTC", Status: StatusAccepted}
	s.Save(o)

	got, ok := s.Get("ord_test_1")
	if !ok || got.Market != "KRW-BTC" {
		t.Errorf("Get 결과 = %+v, ok=%v", got, ok)
	}

	if _, ok := s.Get("존재하지-않음"); ok {
		t.Error("없는 주문은 ok=false여야 함")
	}
}

func TestStoreSaveDefaultsRemainingQuantityToQuantity(t *testing.T) {
	s := NewStore()
	o := &Order{OrderID: "ord_test_1", Market: "KRW-BTC", Quantity: "1.5", Status: StatusAccepted}
	s.Save(o)

	got, _ := s.Get("ord_test_1")
	if got.RemainingQuantity != "1.5" {
		t.Errorf("RemainingQuantity = %q, want 1.5", got.RemainingQuantity)
	}
}

func TestApplyFillPartialKeepsAcceptedButUpgradesToPartiallyFilled(t *testing.T) {
	s := NewStore()
	s.Save(&Order{OrderID: "ord_1", Quantity: "1.0", Status: StatusAccepted})

	found := s.ApplyFill("ord_1", "0.4")
	if !found {
		t.Fatal("found = false, want true")
	}

	got, _ := s.Get("ord_1")
	if got.Status != StatusPartiallyFilled {
		t.Errorf("Status = %q, want %q", got.Status, StatusPartiallyFilled)
	}
	if got.RemainingQuantity != "0.6" {
		t.Errorf("RemainingQuantity = %q, want 0.6", got.RemainingQuantity)
	}
}

func TestApplyFillCompletingFillSetsFilled(t *testing.T) {
	s := NewStore()
	s.Save(&Order{OrderID: "ord_1", Quantity: "1.0", Status: StatusAccepted})

	s.ApplyFill("ord_1", "0.6")
	s.ApplyFill("ord_1", "0.4")

	got, _ := s.Get("ord_1")
	if got.Status != StatusFilled {
		t.Errorf("Status = %q, want %q", got.Status, StatusFilled)
	}
	if got.RemainingQuantity != "0" {
		t.Errorf("RemainingQuantity = %q, want 0", got.RemainingQuantity)
	}
}

// TestApplyFillCompletingFillOverridesStaleCanceled — orders와 executions는
// 서로 독립된 컨슈머라 도착 순서가 실제 발생 순서와 다를 수 있습니다. 뒤늦은
// 취소가 먼저 반영된 뒤 완결시키는 체결이 나중에 와도, 체결이 실제로 일어났다는
// 확증이 더 강하므로 FILLED가 이겨야 합니다(recorder/store/mysql.go의
// updateFill과 같은 원칙).
func TestApplyFillCompletingFillOverridesStaleCanceled(t *testing.T) {
	s := NewStore()
	s.Save(&Order{OrderID: "ord_1", Quantity: "1.0", Status: StatusAccepted})

	o, _ := s.Get("ord_1")
	o.Status = StatusCanceled
	o.CanceledAt = "2026-08-10T00:00:00.000Z"
	o.CanceledQuantity = "1.0"

	found := s.ApplyFill("ord_1", "1.0")
	if !found {
		t.Fatal("found = false, want true")
	}

	got, _ := s.Get("ord_1")
	if got.Status != StatusFilled {
		t.Errorf("Status = %q, want %q (완결 체결이 오래된 취소를 덮어써야 함)", got.Status, StatusFilled)
	}
	if got.CanceledAt != "" || got.CanceledQuantity != "" {
		t.Errorf("CanceledAt/CanceledQuantity = %q/%q, want 빈 문자열 (FILLED로 덮어쓸 때 같이 지워야 함)", got.CanceledAt, got.CanceledQuantity)
	}
}

// TestApplyFillNonCompletingFillDoesNotUndoCanceled — 정상적인 "부분체결 후
// 취소" 케이스를 보호합니다: 이미 취소된 주문에 완결시키지 못하는 체결이 와도
// CANCELED 상태를 되돌리면 안 됩니다.
func TestApplyFillNonCompletingFillDoesNotUndoCanceled(t *testing.T) {
	s := NewStore()
	s.Save(&Order{OrderID: "ord_1", Quantity: "1.0", Status: StatusAccepted})

	s.ApplyFill("ord_1", "0.4") // 남은 0.6

	o, _ := s.Get("ord_1")
	o.Status = StatusCanceled
	o.CanceledAt = "2026-08-10T00:00:00.000Z"
	o.CanceledQuantity = o.RemainingQuantity

	s.ApplyFill("ord_1", "0.1") // 완결 아님(남은 0.5) — CANCELED를 되돌리면 안 됨

	got, _ := s.Get("ord_1")
	if got.Status != StatusCanceled {
		t.Errorf("Status = %q, want %q (완결 아닌 체결이 CANCELED를 되돌리면 안 됨)", got.Status, StatusCanceled)
	}
	if got.CanceledAt == "" {
		t.Error("CanceledAt이 지워지면 안 됨")
	}
}

func TestApplyFillUnknownOrderIDReturnsNotFound(t *testing.T) {
	s := NewStore()
	if found := s.ApplyFill("존재하지-않음", "1.0"); found {
		t.Error("found = true, want false (orderapi 재시작 등으로 없을 수 있는 정상 케이스)")
	}
}
