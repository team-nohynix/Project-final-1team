package bot

import (
	"reflect"
	"testing"
)

func TestBuildSnapshotSkipsMarketsWithNoHistory(t *testing.T) {
	withPrice := NewMarketState(3)
	withPrice.Update(100)

	empty := NewMarketState(3)

	states := map[string]*MarketState{
		"KRW-BTC": withPrice,
		"KRW-ETH": empty,
	}

	got := buildSnapshot(states)
	want := []marketSnapshot{{Market: "KRW-BTC", Prices: []float64{100}}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("buildSnapshot() = %+v, want %+v", got, want)
	}
}

func TestBuildSnapshotSortsByMarketName(t *testing.T) {
	a := NewMarketState(3)
	a.Update(1)
	b := NewMarketState(3)
	b.Update(2)

	states := map[string]*MarketState{
		"KRW-ETH": a,
		"KRW-BTC": b,
	}

	got := buildSnapshot(states)
	if len(got) != 2 || got[0].Market != "KRW-BTC" || got[1].Market != "KRW-ETH" {
		t.Errorf("buildSnapshot()의 마켓 순서 = %v, want [KRW-BTC, KRW-ETH]", got)
	}
}

func TestBuildSnapshotEmptyStatesReturnsEmpty(t *testing.T) {
	if got := buildSnapshot(map[string]*MarketState{}); len(got) != 0 {
		t.Errorf("buildSnapshot({}) = %v, want 빈 슬라이스", got)
	}
}

func TestParseDecisionsValidEntries(t *testing.T) {
	args := decisionArgs{Decisions: []decisionArg{
		{Market: "KRW-BTC", Side: "BUY", Price: 90000000, Quantity: 0.01},
		{Market: "KRW-ETH", Side: "SELL", Price: 5000000, Quantity: 0.1},
	}}

	got, skipped := parseDecisions(args)
	if skipped != 0 {
		t.Errorf("skipped = %d, want 0", skipped)
	}
	want := []GlobalDecision{
		{Market: "KRW-BTC", Decision: Decision{Side: "BUY", Price: 90000000, Quantity: 0.01}},
		{Market: "KRW-ETH", Decision: Decision{Side: "SELL", Price: 5000000, Quantity: 0.1}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseDecisions() = %+v, want %+v", got, want)
	}
}

func TestParseDecisionsSkipsInvalidEntriesButKeepsValidOnes(t *testing.T) {
	args := decisionArgs{Decisions: []decisionArg{
		{Market: "KRW-BTC", Side: "BUY", Price: 90000000, Quantity: 0.01}, // 정상
		{Market: "", Side: "BUY", Price: 100, Quantity: 1},                // market 없음
		{Market: "KRW-ETH", Side: "HOLD", Price: 100, Quantity: 1},        // side 이상
		{Market: "KRW-XRP", Side: "SELL", Price: 0, Quantity: 1},          // price 0
		{Market: "KRW-SOL", Side: "SELL", Price: 100, Quantity: 0},        // quantity 0
	}}

	got, skipped := parseDecisions(args)
	if skipped != 4 {
		t.Errorf("skipped = %d, want 4", skipped)
	}
	if len(got) != 1 || got[0].Market != "KRW-BTC" {
		t.Errorf("parseDecisions() 유효 항목 = %+v, want KRW-BTC 하나만", got)
	}
}

func TestParseDecisionsEmptyIsEmpty(t *testing.T) {
	got, skipped := parseDecisions(decisionArgs{})
	if len(got) != 0 || skipped != 0 {
		t.Errorf("parseDecisions({}) = %v, %d, want 빈 값", got, skipped)
	}
}
