package order

import (
	"context"
	"errors"
	"sync"
	"testing"

	"trader/bot"
)

func TestInMemoryRecorderAccumulatesPerMarket(t *testing.T) {
	r := NewInMemoryRecorder()
	r.Record(NewOrder("KRW-BTC", bot.Decision{Side: "BUY", Price: 100, Quantity: 1}), "ord_1")
	r.Record(NewOrder("KRW-BTC", bot.Decision{Side: "SELL", Price: 101, Quantity: 1}), "ord_2")
	r.Record(NewOrder("KRW-ETH", bot.Decision{Side: "BUY", Price: 50, Quantity: 1}), "ord_3")

	btc := r.Snapshot("KRW-BTC")
	if len(btc) != 2 {
		t.Fatalf("KRW-BTC 기록 개수 = %d, want 2", len(btc))
	}
	if btc[0].Side != "BUY" || btc[1].Side != "SELL" {
		t.Errorf("기록 순서가 안 맞음: %+v", btc)
	}

	eth := r.Snapshot("KRW-ETH")
	if len(eth) != 1 {
		t.Fatalf("KRW-ETH 기록 개수 = %d, want 1", len(eth))
	}

	if got := r.Snapshot("KRW-XRP"); len(got) != 0 {
		t.Errorf("기록한 적 없는 마켓은 빈 슬라이스여야 하는데 %v", got)
	}
}

func TestInMemoryRecorderSnapshotSortsByTS(t *testing.T) {
	r := NewInMemoryRecorder()
	// Record 호출 순서를 ts 역순으로 만들어, Snapshot이 호출 순서가 아니라 ts로
	// 정렬함을 확인한다 (동시 고루틴이 ts 순서와 다르게 도착할 수 있어서 필요).
	r.Record(Order{Market: "KRW-BTC", TS: 300, Side: "SELL"}, "ord_1")
	r.Record(Order{Market: "KRW-BTC", TS: 100, Side: "BUY"}, "ord_2")
	r.Record(Order{Market: "KRW-BTC", TS: 200, Side: "BUY"}, "ord_3")

	got := r.Snapshot("KRW-BTC")
	wantTS := []int64{100, 200, 300}
	for i, want := range wantTS {
		if got[i].TS != want {
			t.Errorf("Snapshot()[%d].TS = %d, want %d (전체: %+v)", i, got[i].TS, want, got)
		}
	}
}

func TestInMemoryRecorderSnapshotIsACopy(t *testing.T) {
	r := NewInMemoryRecorder()
	r.Record(NewOrder("KRW-BTC", bot.Decision{Side: "BUY", Price: 100, Quantity: 1}), "ord_1")

	snap := r.Snapshot("KRW-BTC")
	snap[0].Side = "MUTATED"

	if got := r.Snapshot("KRW-BTC"); got[0].Side != "BUY" {
		t.Errorf("Snapshot을 수정해도 내부 상태가 바뀌면 안 되는데: %+v", got)
	}
}

func TestInMemoryRecorderConcurrentRecord(t *testing.T) {
	r := NewInMemoryRecorder()
	var wg sync.WaitGroup
	for range 100 {
		wg.Go(func() {
			r.Record(NewOrder("KRW-BTC", bot.Decision{Side: "BUY", Price: 100, Quantity: 1}), "ord_1")
		})
	}
	wg.Wait()

	if got := len(r.Snapshot("KRW-BTC")); got != 100 {
		t.Errorf("동시 기록 100건 중 %d건만 남음(경합 조건 의심)", got)
	}
}

type stubSubmitter struct {
	orderID string
	err     error
}

func (s stubSubmitter) Submit(context.Context, Order) (string, error) { return s.orderID, s.err }

func TestRecordingSubmitterRecordsOnlyOnSuccess(t *testing.T) {
	recorder := NewInMemoryRecorder()
	o := NewOrder("KRW-BTC", bot.Decision{Side: "BUY", Price: 100, Quantity: 1})

	ok := RecordingSubmitter{Next: stubSubmitter{orderID: "ord_1", err: nil}, Recorder: recorder}
	orderID, err := ok.Submit(context.Background(), o)
	if err != nil {
		t.Fatalf("성공 케이스에서 에러: %v", err)
	}
	if orderID != "ord_1" {
		t.Errorf("orderID = %q, want ord_1", orderID)
	}
	got := recorder.Snapshot("KRW-BTC")
	if len(got) != 1 {
		t.Fatalf("제출 성공 시 1건 기록돼야 하는데 %d건", len(got))
	}
	if got[0].OrderID != "ord_1" {
		t.Errorf("기록된 OrderID = %q, want ord_1", got[0].OrderID)
	}

	failing := errors.New("제출 실패")
	fail := RecordingSubmitter{Next: stubSubmitter{err: failing}, Recorder: recorder}
	if _, err := fail.Submit(context.Background(), o); !errors.Is(err, failing) {
		t.Errorf("err = %v, want %v", err, failing)
	}
	if got := len(recorder.Snapshot("KRW-BTC")); got != 1 {
		t.Errorf("제출 실패 시 추가 기록되면 안 되는데 %d건(기록 건수와 접수 건수 불일치)", got)
	}
}
