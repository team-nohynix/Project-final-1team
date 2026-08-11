package dataset

import (
	"sort"
	"time"

	"backend/upbit"
)

// BuildBatch는 일/주/월/년봉 데이터를 배치 파일 구조로 변환합니다.
func BuildBatch(market string, start, end time.Time, days, weeks, months, years []upbit.Candle) BatchFile {
	return BatchFile{
		Market: market,
		Range:  toRange(start, end),
		Candles: BatchCandles{
			Days:   toCandleOHLCVs(days),
			Weeks:  toCandleOHLCVs(weeks),
			Months: toCandleOHLCVs(months),
			Years:  toCandleOHLCVs(years),
		},
	}
}

// BuildStream은 초/분봉과 개별 체결을 ts 오름차순으로 병합해 스트림 파일 구조로 변환합니다.
func BuildStream(market string, start, end time.Time, seconds, minutes []upbit.Candle, ticks []upbit.TradeTick) StreamFile {
	events := make([]StreamEvent, 0, len(seconds)+len(minutes)+len(ticks))

	for _, c := range seconds {
		events = append(events, candleEvent("candle_second", c))
	}
	for _, c := range minutes {
		events = append(events, candleEvent("candle_minute", c))
	}
	for _, t := range ticks {
		events = append(events, StreamEvent{
			Type:   "trade_tick",
			TS:     t.Timestamp,
			Price:  t.TradePrice,
			Volume: t.TradeVolume,
			Side:   mapSide(t.AskBid),
		})
	}

	sort.Slice(events, func(i, j int) bool { return events[i].TS < events[j].TS })

	return StreamFile{
		Market: market,
		Range:  toRange(start, end),
		Events: events,
	}
}

// toRange는 start/end를 KST로 표시합니다 — 날짜 경계 자체가 KST 기준이라
// (server.go의 parseDate), 파일명(formatFileTime)과 일치하도록 range 필드도
// 동일하게 KST로 표시합니다. RFC3339에 KST 오프셋(+09:00)이 그대로 붙어
// 나오므로 여전히 명확하고 파싱 가능한 형식입니다.
func toRange(start, end time.Time) Range {
	return Range{
		Start: start.In(upbit.KST).Format(time.RFC3339),
		End:   end.In(upbit.KST).Format(time.RFC3339),
	}
}

func toCandleOHLCVs(candles []upbit.Candle) []CandleOHLCV {
	out := make([]CandleOHLCV, 0, len(candles))
	for _, c := range candles {
		out = append(out, CandleOHLCV{
			TS:     c.Timestamp,
			Open:   c.OpeningPrice,
			High:   c.HighPrice,
			Low:    c.LowPrice,
			Close:  c.TradePrice,
			Volume: c.CandleAccTradeVolume,
		})
	}
	return out
}

func candleEvent(eventType string, c upbit.Candle) StreamEvent {
	return StreamEvent{
		Type:   eventType,
		TS:     c.Timestamp,
		Open:   c.OpeningPrice,
		High:   c.HighPrice,
		Low:    c.LowPrice,
		Close:  c.TradePrice,
		Volume: c.CandleAccTradeVolume,
	}
}

// mapSide는 업비트의 ASK/BID를 CLAUDE.md에 정의된 SELL/BUY로 변환합니다.
func mapSide(askBid string) string {
	switch askBid {
	case "BID":
		return "BUY"
	case "ASK":
		return "SELL"
	default:
		return askBid
	}
}
