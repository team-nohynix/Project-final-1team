package main

import (
	"context"
	"sync"
	"testing"

	"replayengine/orderstore"
)

type fakeSubmitter struct {
	mu      sync.Mutex
	submits []orderstore.RecordedOrder
	markets []string
}

func (f *fakeSubmitter) Submit(_ context.Context, market string, o orderstore.RecordedOrder) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.submits = append(f.submits, o)
	f.markets = append(f.markets, market)
	return nil
}

func TestFilterRangeNoLimitsReturnsAll(t *testing.T) {
	orders := []orderstore.RecordedOrder{{TS: 1}, {TS: 2}, {TS: 3}}
	got := filterRange(orders, 0, 0)
	if len(got) != 3 {
		t.Errorf("len(got) = %d, want 3", len(got))
	}
}

func TestFilterRangeAppliesFromAndTo(t *testing.T) {
	orders := []orderstore.RecordedOrder{{TS: 100}, {TS: 200}, {TS: 300}, {TS: 400}}
	got := filterRange(orders, 150, 350)
	if len(got) != 2 || got[0].TS != 200 || got[1].TS != 300 {
		t.Errorf("got = %+v", got)
	}
}

func TestReplayMarketSubmitsInOrder(t *testing.T) {
	orders := []orderstore.RecordedOrder{
		{TS: 1000, Side: "BUY", Price: "100", Quantity: "1"},
		{TS: 1001, Side: "SELL", Price: "101", Quantity: "2"},
		{TS: 1002, Side: "BUY", Price: "102", Quantity: "3"},
	}
	sub := &fakeSubmitter{}

	// speed를 아주 크게 잡아서 페이싱 대기가 테스트를 느리게 만들지 않게 합니다.
	replayMarket(context.Background(), "KRW-BTC", orders, 1_000_000, sub)

	if len(sub.submits) != 3 {
		t.Fatalf("제출 건수 = %d, want 3", len(sub.submits))
	}
	for i, o := range sub.submits {
		if o.TS != orders[i].TS {
			t.Errorf("submits[%d].TS = %d, want %d (순서 보존)", i, o.TS, orders[i].TS)
		}
		if sub.markets[i] != "KRW-BTC" {
			t.Errorf("markets[%d] = %q, want KRW-BTC", i, sub.markets[i])
		}
	}
}

func TestReplayMarketStopsOnContextCancel(t *testing.T) {
	orders := []orderstore.RecordedOrder{
		{TS: 0, Side: "BUY", Price: "100", Quantity: "1"},
		{TS: 1_000_000, Side: "SELL", Price: "101", Quantity: "2"}, // 아주 먼 다음 이벤트 — 대기 중 취소돼야 함
	}
	sub := &fakeSubmitter{}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 시작 전에 이미 취소 — 첫 이벤트는 제출되고 두 번째는 대기 중 멈춰야 함

	replayMarket(ctx, "KRW-BTC", orders, 1, sub)

	if len(sub.submits) != 1 {
		t.Errorf("취소 후에는 첫 이벤트만 제출되고 멈춰야 하는데 제출 건수 = %d", len(sub.submits))
	}
}
