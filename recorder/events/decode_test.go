package events

import "testing"

func TestDecodeOrderEventNew(t *testing.T) {
	data := []byte(`{"type":"NEW","orderId":"ord_1","market":"KRW-BTC","side":"BUY","price":"100","quantity":"1","acceptedAt":"2026-08-06T00:00:00.000Z","clientRequestId":"key-1","mode":"PAPER_TRADING"}`)
	ev, err := DecodeOrderEvent(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ev.Type != OrderNew || ev.OrderID != "ord_1" || ev.Mode != "PAPER_TRADING" || ev.ClientRequestID != "key-1" {
		t.Errorf("got = %+v", ev)
	}
}

func TestDecodeOrderEventCancelHasOnlyIDAndMarketAndCanceledAt(t *testing.T) {
	data := []byte(`{"type":"CANCEL","orderId":"ord_1","market":"KRW-BTC","canceledAt":"2026-08-06T00:00:01.000Z"}`)
	ev, err := DecodeOrderEvent(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ev.Type != OrderCancel || ev.OrderID != "ord_1" || ev.CanceledAt != "2026-08-06T00:00:01.000Z" {
		t.Errorf("got = %+v", ev)
	}
	if ev.Side != "" || ev.Price != "" || ev.Mode != "" {
		t.Errorf("CANCEL 이벤트는 Side/Price/Mode가 비어 있어야 하는데 got = %+v", ev)
	}
}

func TestDecodeOrderEventUnknownTypeFails(t *testing.T) {
	_, err := DecodeOrderEvent([]byte(`{"type":"WEIRD","orderId":"ord_1"}`))
	if err == nil {
		t.Fatal("알 수 없는 타입인데 에러가 안 남")
	}
}

func TestDecodeOrderEventMalformedJSONFails(t *testing.T) {
	_, err := DecodeOrderEvent([]byte(`not json`))
	if err == nil {
		t.Fatal("깨진 JSON인데 에러가 안 남")
	}
}

func TestDecodeExecution(t *testing.T) {
	data := []byte(`{"market":"KRW-BTC","buyOrderId":"b1","sellOrderId":"s1","price":"100","quantity":"0.5"}`)
	ev, err := DecodeExecution(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ev.Market != "KRW-BTC" || ev.BuyOrderID != "b1" || ev.SellOrderID != "s1" || ev.Quantity != "0.5" {
		t.Errorf("got = %+v", ev)
	}
}

func TestDecodeExecutionMalformedJSONFails(t *testing.T) {
	_, err := DecodeExecution([]byte(`not json`))
	if err == nil {
		t.Fatal("깨진 JSON인데 에러가 안 남")
	}
}

func TestDecodeAssignmentAssigned(t *testing.T) {
	data := []byte(`{"type":"ASSIGNED","market":"KRW-BTC","engineInstanceId":"engine_1","at":"2026-08-07T00:00:00.000Z"}`)
	ev, err := DecodeAssignment(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ev.Type != AssignmentAssigned || ev.Market != "KRW-BTC" || ev.EngineInstanceID != "engine_1" {
		t.Errorf("got = %+v", ev)
	}
}

func TestDecodeAssignmentUnknownTypeFails(t *testing.T) {
	_, err := DecodeAssignment([]byte(`{"type":"WEIRD","market":"KRW-BTC"}`))
	if err == nil {
		t.Fatal("알 수 없는 타입인데 에러가 안 남")
	}
}

func TestDecodeAssignmentMalformedJSONFails(t *testing.T) {
	_, err := DecodeAssignment([]byte(`not json`))
	if err == nil {
		t.Fatal("깨진 JSON인데 에러가 안 남")
	}
}
