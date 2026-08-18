package archive

import "testing"

// fakeStore는 실제 로컬/S3 I/O 없이 Batcher를 검증하기 위한 Store 구현체입니다.
type fakeStore struct {
	saves [][]any
	kinds []string
}

func (f *fakeStore) Save(kind string, records []any) (string, error) {
	f.kinds = append(f.kinds, kind)
	f.saves = append(f.saves, records)
	return "fake://" + kind, nil
}

func TestBatcherFlushesOnCountTrigger(t *testing.T) {
	store := &fakeStore{}
	b := NewBatcher(store, "orders", 3)

	b.Add(1)
	b.Add(2)
	if len(store.saves) != 0 {
		t.Fatalf("건수 트리거(3) 도달 전인데 이미 저장됨: %+v", store.saves)
	}

	b.Add(3)
	if len(store.saves) != 1 {
		t.Fatalf("건수 트리거 도달 시 1회 저장돼야 하는데 %d회", len(store.saves))
	}
	if len(store.saves[0]) != 3 {
		t.Errorf("저장된 배치 크기 = %d, want 3", len(store.saves[0]))
	}
	if store.kinds[0] != "orders" {
		t.Errorf("kind = %q, want orders", store.kinds[0])
	}
}

func TestBatcherManualFlushSendsPartialBatch(t *testing.T) {
	store := &fakeStore{}
	b := NewBatcher(store, "executions", 100)

	b.Add("a")
	b.Add("b")
	b.Flush() // 시간 트리거를 흉내(main.go의 주기 타이머가 이렇게 호출함)

	if len(store.saves) != 1 || len(store.saves[0]) != 2 {
		t.Fatalf("got saves = %+v", store.saves)
	}
}

func TestBatcherFlushOnEmptyBufferIsNoOp(t *testing.T) {
	store := &fakeStore{}
	b := NewBatcher(store, "orders", 10)

	b.Flush()
	if len(store.saves) != 0 {
		t.Errorf("빈 버퍼를 플러시했는데 저장이 호출됨: %+v", store.saves)
	}
}

func TestBatcherBufferResetsAfterFlush(t *testing.T) {
	store := &fakeStore{}
	b := NewBatcher(store, "orders", 2)

	b.Add(1)
	b.Add(2) // 플러시됨
	b.Add(3)
	b.Flush()

	if len(store.saves) != 2 {
		t.Fatalf("저장 횟수 = %d, want 2", len(store.saves))
	}
	if len(store.saves[1]) != 1 {
		t.Errorf("두 번째 배치 크기 = %d, want 1 (플러시 후 버퍼가 리셋돼야 함)", len(store.saves[1]))
	}
}
