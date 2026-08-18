package dataset

import (
	"testing"
	"time"

	"backend/upbit"
)

func TestBuildBatchMapsCandleFields(t *testing.T) {
	start := time.Date(2026, 7, 27, 0, 0, 0, 0, upbit.KST)
	end := start.Add(24 * time.Hour)

	days := []upbit.Candle{{
		Timestamp:            1000,
		OpeningPrice:         100,
		HighPrice:            110,
		LowPrice:             90,
		TradePrice:           105, // 종가
		CandleAccTradeVolume: 42,
	}}

	batch := BuildBatch("KRW-BTC", start, end, days, nil, nil, nil)

	if batch.Market != "KRW-BTC" {
		t.Errorf("Market = %q, want KRW-BTC", batch.Market)
	}
	if batch.Range.Start != "2026-07-27T00:00:00+09:00" || batch.Range.End != "2026-07-28T00:00:00+09:00" {
		t.Errorf("Range = %+v (KST로 표시돼야 함)", batch.Range)
	}
	if len(batch.Candles.Days) != 1 {
		t.Fatalf("Days 길이 = %d, want 1", len(batch.Candles.Days))
	}

	got := batch.Candles.Days[0]
	want := CandleOHLCV{TS: 1000, Open: 100, High: 110, Low: 90, Close: 105, Volume: 42}
	if got != want {
		t.Errorf("Days[0] = %+v, want %+v (trade_price -> close 매핑 확인)", got, want)
	}

	if len(batch.Candles.Weeks) != 0 || len(batch.Candles.Months) != 0 || len(batch.Candles.Years) != 0 {
		t.Errorf("전달 안 한 단위는 비어 있어야 하는데: %+v", batch.Candles)
	}
}

func TestBuildStreamMergesAndSortsByTS(t *testing.T) {
	start := time.Date(2026, 7, 27, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)

	seconds := []upbit.Candle{{Timestamp: 300, OpeningPrice: 1, HighPrice: 1, LowPrice: 1, TradePrice: 1, CandleAccTradeVolume: 1}}
	minutes := []upbit.Candle{{Timestamp: 100, OpeningPrice: 2, HighPrice: 2, LowPrice: 2, TradePrice: 2, CandleAccTradeVolume: 2}}
	ticks := []upbit.TradeTick{
		{Timestamp: 200, TradePrice: 50000, TradeVolume: 0.5, AskBid: "BID"},
		{Timestamp: 400, TradePrice: 50100, TradeVolume: 0.1, AskBid: "ASK"},
	}

	stream := BuildStream("KRW-BTC", start, end, seconds, minutes, ticks)

	if len(stream.Events) != 4 {
		t.Fatalf("Events 길이 = %d, want 4", len(stream.Events))
	}

	// ts 오름차순(100, 200, 300, 400)으로 정렬돼야 하고, 타입이 섞여 들어와도 유지돼야 한다.
	wantOrder := []struct {
		ts  int64
		typ string
	}{
		{100, "candle_minute"},
		{200, "trade_tick"},
		{300, "candle_second"},
		{400, "trade_tick"},
	}
	for i, w := range wantOrder {
		if stream.Events[i].TS != w.ts || stream.Events[i].Type != w.typ {
			t.Errorf("Events[%d] = {ts:%d type:%s}, want {ts:%d type:%s}",
				i, stream.Events[i].TS, stream.Events[i].Type, w.ts, w.typ)
		}
	}

	// trade_tick 필드 매핑: trade_price -> price(캔들의 close와는 다른 의미), ask_bid -> side(BID->BUY, ASK->SELL)
	tick1 := stream.Events[1]
	if tick1.Price != 50000 || tick1.Volume != 0.5 || tick1.Side != "BUY" {
		t.Errorf("첫 체결 이벤트 = %+v, price=50000 volume=0.5 side=BUY 를 기대함", tick1)
	}
	tick2 := stream.Events[3]
	if tick2.Side != "SELL" {
		t.Errorf("ASK는 SELL로 매핑돼야 하는데 side=%q", tick2.Side)
	}
}

func TestMapSidePassesThroughUnknownValues(t *testing.T) {
	// 업비트가 BID/ASK 이외 값을 준 적은 없지만, 매핑표에 없는 값은 그대로 통과시켜
	// 조용히 잘못된 값으로 둔갑하지 않게 한다(원본을 그대로 남겨 문제를 드러냄).
	if got := mapSide("UNKNOWN"); got != "UNKNOWN" {
		t.Errorf("mapSide(UNKNOWN) = %q, want UNKNOWN", got)
	}
}
