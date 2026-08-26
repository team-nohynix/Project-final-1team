package order

import (
	"context"
	"testing"

	"trader/bot"
)

func TestNewOrderRoundsPriceToTick(t *testing.T) {
	o := NewOrder("KRW-RE", bot.Decision{Side: "BUY", Price: 632.732, Quantity: 0.01})

	if o.Market != "KRW-RE" || o.Side != "BUY" {
		t.Fatalf("market/side가 그대로 전달되지 않음: %+v", o)
	}
	if o.Price != "632.7" {
		t.Errorf("Price = %q, tick 보정된 632.7을 기대함", o.Price)
	}
	if o.Quantity != "0.01000000" {
		t.Errorf("Quantity = %q, want 0.01000000 (8자리 고정)", o.Quantity)
	}
}

func TestNewOrderQuantityNeverExceedsEightDecimals(t *testing.T) {
	// 실전 사고 재현: 봇의 float64 연산 결과는 강제 없이 그대로 문자열화하면
	// (strconv.FormatFloat(..., -1, 64)) 8자리보다 긴 소수가 나올 수 있었다 —
	// 이게 부분체결마다 DB(DECIMAL(24,8))에 독립적으로 반올림되면서 "초과체결"
	// 정합성 오류로 이어졌다(2026-08-26). 여기서는 그 원인이었던 값(0.1+0.2 같은
	// 전형적인 float64 표현 오차)을 그대로 넣어 8자리로 고정되는지 확인한다.
	o := NewOrder("KRW-BTC", bot.Decision{Side: "BUY", Price: 100, Quantity: 0.1 + 0.2})
	if o.Quantity != "0.30000000" {
		t.Errorf("Quantity = %q, want 0.30000000", o.Quantity)
	}
}

func TestLogOnlySubmitterNeverErrors(t *testing.T) {
	var s OrderSubmitter = LogOnlySubmitter{}
	o := NewOrder("KRW-BTC", bot.Decision{Side: "SELL", Price: 90_000_000, Quantity: 0.001})

	if _, err := s.Submit(context.Background(), o); err != nil {
		t.Errorf("LogOnlySubmitter.Submit 은 항상 nil을 반환해야 하는데: %v", err)
	}
}
