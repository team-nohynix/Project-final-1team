package bot

import (
	"reflect"
	"testing"
)

func TestMarketStatePriceBeforeAnyUpdate(t *testing.T) {
	s := NewMarketState(3)
	if _, ok := s.Price(); ok {
		t.Error("갱신 전에는 ok=false여야 함")
	}
	if h := s.History(); len(h) != 0 {
		t.Errorf("갱신 전 History()는 비어 있어야 하는데 len=%d", len(h))
	}
}

func TestMarketStatePriceReturnsLatest(t *testing.T) {
	s := NewMarketState(3)
	s.Update(10)
	s.Update(20)
	s.Update(30)

	price, ok := s.Price()
	if !ok || price != 30 {
		t.Errorf("Price() = %v, %v; want 30, true", price, ok)
	}
}

func TestMarketStateHistoryBeforeBufferFull(t *testing.T) {
	s := NewMarketState(5)
	s.Update(1)
	s.Update(2)

	got := s.History()
	want := []float64{1, 2}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("History() = %v, want %v", got, want)
	}
}

func TestMarketStateHistoryWrapsAroundCircularBuffer(t *testing.T) {
	s := NewMarketState(3)
	// 버퍼 크기(3)를 넘겨서 채운다 — 가장 오래된 값(1)은 밀려나야 한다.
	s.Update(1)
	s.Update(2)
	s.Update(3)
	s.Update(4)
	s.Update(5)

	got := s.History()
	want := []float64{3, 4, 5} // 오래된 것 -> 최신 순
	if !reflect.DeepEqual(got, want) {
		t.Errorf("History() = %v, want %v (원형 버퍼가 한 바퀴 돈 뒤 순서가 안 맞음)", got, want)
	}

	price, ok := s.Price()
	if !ok || price != 5 {
		t.Errorf("Price() = %v, %v; want 5, true", price, ok)
	}
}

func TestMarketMakerBotQuotesAroundCurrentPrice(t *testing.T) {
	s := NewMarketState(PriceHistorySize)
	s.Update(1000)

	decisions := MarketMakerBot{}.Decide(s)
	if len(decisions) != 2 {
		t.Fatalf("마켓메이커는 매수+매도 2건을 내야 하는데 %d건", len(decisions))
	}

	var buy, sell *Decision
	for i := range decisions {
		switch decisions[i].Side {
		case "BUY":
			buy = &decisions[i]
		case "SELL":
			sell = &decisions[i]
		}
	}
	if buy == nil || sell == nil {
		t.Fatalf("BUY/SELL이 하나씩 있어야 하는데: %+v", decisions)
	}
	if buy.Price >= 1000 {
		t.Errorf("매수 호가(%v)는 현재가(1000)보다 낮아야 함", buy.Price)
	}
	if sell.Price <= 1000 {
		t.Errorf("매도 호가(%v)는 현재가(1000)보다 높아야 함", sell.Price)
	}
}

func TestMarketMakerBotNoOpWithoutPrice(t *testing.T) {
	s := NewMarketState(PriceHistorySize) // 아직 한 번도 Update 안 함
	if got := (MarketMakerBot{}).Decide(s); got != nil {
		t.Errorf("가격을 모르면 판단하면 안 되는데 %v를 반환함", got)
	}
}
