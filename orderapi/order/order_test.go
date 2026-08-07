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
