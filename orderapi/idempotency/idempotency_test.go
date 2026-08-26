package idempotency

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestStorePutAndGet(t *testing.T) {
	s := NewStore()
	if _, ok := s.Get("key-1"); ok {
		t.Fatal("아직 아무것도 안 넣었는데 조회되면 안 됨")
	}

	s.Put("key-1", 202, []byte(`{"orderId":"ord_1"}`))
	got, ok := s.Get("key-1")
	if !ok {
		t.Fatal("Put한 키는 조회돼야 함")
	}
	if got.Status != 202 || string(got.Body) != `{"orderId":"ord_1"}` {
		t.Errorf("got = %+v", got)
	}
}

func TestStoreDoesNotOverwrite(t *testing.T) {
	s := NewStore()
	s.Put("key-1", 202, []byte("first"))
	s.Put("key-1", 500, []byte("second")) // 같은 키로 다시 Put — 무시돼야 함

	got, _ := s.Get("key-1")
	if got.Status != 202 || string(got.Body) != "first" {
		t.Errorf("최초 응답이 유지돼야 하는데 got = %+v", got)
	}
}

// TestSweepRemovesOnlyEntriesOlderThanMaxAge — order.Store.Sweep과 같은 계약
// (2026-08-27 메모리 축출 사고 대응).
func TestSweepRemovesOnlyEntriesOlderThanMaxAge(t *testing.T) {
	s := NewStore()
	s.Put("key-old", 202, []byte("old"))
	time.Sleep(20 * time.Millisecond)
	s.Put("key-new", 202, []byte("new"))

	removed := s.Sweep(10 * time.Millisecond)
	if removed != 1 {
		t.Errorf("removed = %d, want 1", removed)
	}
	if _, ok := s.Get("key-old"); ok {
		t.Error("key-old는 지워졌어야 함")
	}
	if _, ok := s.Get("key-new"); !ok {
		t.Error("key-new는 남아있어야 함")
	}
}

func TestSweepWithGenerousMaxAgeKeepsEverything(t *testing.T) {
	s := NewStore()
	s.Put("key-1", 202, []byte("a"))
	s.Put("key-2", 202, []byte("b"))

	if removed := s.Sweep(1 * time.Hour); removed != 0 {
		t.Errorf("removed = %d, want 0", removed)
	}
	if _, ok := s.Get("key-1"); !ok {
		t.Error("key-1이 지워지면 안 됨")
	}
}

// TestSweepDoesNotBreakDoNotOverwriteInvariant — 지워진 뒤엔 같은 키로 다시 Put하면
// "최초 응답 유지" 규칙이 그 새 Put 기준으로 다시 적용돼야 합니다(지워지기 전
// 값이 유령처럼 남아있으면 안 됨).
// TestConcurrentPutGetSweep — order.Store와 같은 이유의 동시성 스트레스 테스트.
func TestConcurrentPutGetSweep(t *testing.T) {
	s := NewStore()
	var wg sync.WaitGroup

	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			key := fmt.Sprintf("key-%d", i)
			s.Put(key, 202, []byte("body"))
			s.Get(key)
		}(i)
	}
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.Sweep(time.Hour)
		}()
	}
	wg.Wait()
}

func TestSweepDoesNotBreakDoNotOverwriteInvariant(t *testing.T) {
	s := NewStore()
	s.Put("key-1", 202, []byte("first"))
	time.Sleep(20 * time.Millisecond)
	s.Sweep(10 * time.Millisecond)

	s.Put("key-1", 202, []byte("second"))
	got, ok := s.Get("key-1")
	if !ok || string(got.Body) != "second" {
		t.Errorf("Sweep 이후 재요청은 새 최초 응답으로 취급돼야 하는데 got = %+v, ok=%v", got, ok)
	}
}
