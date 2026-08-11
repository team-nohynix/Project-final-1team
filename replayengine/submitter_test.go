package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"replayengine/orderstore"
)

func TestHTTPOrderSubmitterSubmitsAcceptedOrder(t *testing.T) {
	var gotPath string
	var gotBody orderRequest
	var gotKey string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotKey = r.Header.Get("Idempotency-Key")
		json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	s := HTTPOrderSubmitter{Client: srv.Client(), BaseURL: srv.URL}
	o := orderstore.RecordedOrder{TS: 1, Side: "BUY", Price: "90000000", Quantity: "0.01", OrderID: "ord_20260806_0000001"}

	if err := s.Submit(context.Background(), "KRW-BTC", o); err != nil {
		t.Fatalf("Submit 실패: %v", err)
	}
	if gotPath != "/v1/orders" {
		t.Errorf("경로 = %q, want /v1/orders", gotPath)
	}
	if gotKey == "" {
		t.Error("Idempotency-Key 헤더가 비어있음")
	}
	if gotBody.Market != "KRW-BTC" || gotBody.Side != "BUY" || gotBody.Price != o.Price || gotBody.Quantity != o.Quantity {
		t.Errorf("요청 바디 = %+v", gotBody)
	}
	if gotBody.SourceOrderID != o.OrderID {
		t.Errorf("SourceOrderID = %q, want %q (기록 파일의 원본 orderId가 그대로 전달돼야 함)", gotBody.SourceOrderID, o.OrderID)
	}
}

func TestHTTPOrderSubmitterReturnsErrorOnNonAccepted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"errorCode":"INVALID_MARKET"}`, http.StatusBadRequest)
	}))
	defer srv.Close()

	s := HTTPOrderSubmitter{Client: srv.Client(), BaseURL: srv.URL}
	o := orderstore.RecordedOrder{TS: 1, Side: "SELL", Price: "90000000", Quantity: "0.01"}

	if err := s.Submit(context.Background(), "KRW-BTC", o); err == nil {
		t.Fatal("400 응답에 대해 에러를 기대했으나 nil")
	}
}

func TestHTTPOrderSubmitterUsesFreshIdempotencyKeyEachCall(t *testing.T) {
	var keys []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		keys = append(keys, r.Header.Get("Idempotency-Key"))
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	s := HTTPOrderSubmitter{Client: srv.Client(), BaseURL: srv.URL}
	o := orderstore.RecordedOrder{TS: 1, Side: "BUY", Price: "90000000", Quantity: "0.01"}

	if err := s.Submit(context.Background(), "KRW-BTC", o); err != nil {
		t.Fatalf("첫 번째 Submit 실패: %v", err)
	}
	if err := s.Submit(context.Background(), "KRW-BTC", o); err != nil {
		t.Fatalf("두 번째 Submit 실패: %v", err)
	}

	if len(keys) != 2 || keys[0] == "" || keys[1] == "" || keys[0] == keys[1] {
		t.Errorf("Idempotency-Key들 = %v, 매 호출마다 서로 다른 비어있지 않은 값을 기대함", keys)
	}
}
