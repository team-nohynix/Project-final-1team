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
	if o.Quantity != "0.01" {
		t.Errorf("Quantity = %q, want 0.01", o.Quantity)
	}
}

func TestLogOnlySubmitterNeverErrors(t *testing.T) {
	var s OrderSubmitter = LogOnlySubmitter{}
	o := NewOrder("KRW-BTC", bot.Decision{Side: "SELL", Price: 90_000_000, Quantity: 0.001})

	if _, err := s.Submit(context.Background(), o); err != nil {
		t.Errorf("LogOnlySubmitter.Submit 은 항상 nil을 반환해야 하는데: %v", err)
	}
}
