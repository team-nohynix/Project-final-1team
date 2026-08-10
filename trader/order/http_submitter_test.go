package order

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"trader/bot"
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
		json.NewEncoder(w).Encode(orderResponse{OrderID: "ord_20260807_0000001"})
	}))
	defer srv.Close()

	s := HTTPOrderSubmitter{Client: srv.Client(), BaseURL: srv.URL}
	o := NewOrder("KRW-BTC", bot.Decision{Side: "BUY", Price: 90_000_000, Quantity: 0.001})

	orderID, err := s.Submit(context.Background(), o)
	if err != nil {
		t.Fatalf("Submit 실패: %v", err)
	}
	if orderID != "ord_20260807_0000001" {
		t.Errorf("orderID = %q, want ord_20260807_0000001", orderID)
	}
	if gotPath != "/v1/orders" {
		t.Errorf("경로 = %q, want /v1/orders", gotPath)
	}
	if gotKey == "" {
		t.Error("Idempotency-Key 헤더가 비어있음")
	}
	if gotBody.Market != "KRW-BTC" || gotBody.Side != "BUY" || gotBody.Price != o.Price || gotBody.Quantity != o.Quantity {
		t.Errorf("요청 바디 = %+v, 주문 %+v와 대응돼야 함", gotBody, o)
	}
}

func TestHTTPOrderSubmitterWraps429AsErrTooManyRequests(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"errorCode":"CONSUMER_LAG_EXCEEDED"}`, http.StatusTooManyRequests)
	}))
	defer srv.Close()

	s := HTTPOrderSubmitter{Client: srv.Client(), BaseURL: srv.URL}
	o := NewOrder("KRW-BTC", bot.Decision{Side: "BUY", Price: 90_000_000, Quantity: 0.001})

	_, err := s.Submit(context.Background(), o)
	if !errors.Is(err, ErrTooManyRequests) {
		t.Errorf("err = %v, want ErrTooManyRequests로 감싸져 있어야 함 (RetryingSubmitter가 errors.Is로 식별)", err)
	}
}

func TestHTTPOrderSubmitterReturnsErrorOnNonAccepted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"errorCode":"INVALID_MARKET"}`, http.StatusBadRequest)
	}))
	defer srv.Close()

	s := HTTPOrderSubmitter{Client: srv.Client(), BaseURL: srv.URL}
	o := NewOrder("KRW-BTC", bot.Decision{Side: "SELL", Price: 90_000_000, Quantity: 0.001})

	if _, err := s.Submit(context.Background(), o); err == nil {
		t.Fatal("400 응답에 대해 에러를 기대했으나 nil")
	}
}

// TestHTTPOrderSubmitterReusesSameKeyOnRepeatedSubmit — Idempotency-Key는 이제
// Order 생성 시점(NewOrder)에 고정되므로, 같은 Order를 여러 번 Submit해도
// (RetryingSubmitter가 재시도할 때 실제로 하는 일) 항상 같은 키가 나가야
// 합니다 — 예전엔 반대로 "호출마다 새 키"가 맞는 동작이었지만, 재시도가
// 도입되면서 뒤집혔습니다(order.go의 Order.IdempotencyKey 설명 참고).
func TestHTTPOrderSubmitterReusesSameKeyOnRepeatedSubmit(t *testing.T) {
	var keys []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		keys = append(keys, r.Header.Get("Idempotency-Key"))
		w.WriteHeader(http.StatusAccepted)
		json.NewEncoder(w).Encode(orderResponse{OrderID: "ord_20260807_0000001"})
	}))
	defer srv.Close()

	s := HTTPOrderSubmitter{Client: srv.Client(), BaseURL: srv.URL}
	o := NewOrder("KRW-BTC", bot.Decision{Side: "BUY", Price: 90_000_000, Quantity: 0.001})

	if _, err := s.Submit(context.Background(), o); err != nil {
		t.Fatalf("첫 번째 Submit 실패: %v", err)
	}
	if _, err := s.Submit(context.Background(), o); err != nil {
		t.Fatalf("두 번째 Submit 실패: %v", err)
	}

	if len(keys) != 2 || keys[0] == "" || keys[1] == "" || keys[0] != keys[1] {
		t.Errorf("Idempotency-Key들 = %v, 같은 Order를 재사용하면 매번 같은 값을 기대함", keys)
	}
}

// TestNewOrderGeneratesFreshKeyPerOrder — NewOrder를 따로 호출할 때마다는
// (=서로 다른 논리적 주문) 서로 다른 키가 나와야 합니다.
func TestNewOrderGeneratesFreshKeyPerOrder(t *testing.T) {
	o1 := NewOrder("KRW-BTC", bot.Decision{Side: "BUY", Price: 90_000_000, Quantity: 0.001})
	o2 := NewOrder("KRW-BTC", bot.Decision{Side: "BUY", Price: 90_000_000, Quantity: 0.001})

	if o1.IdempotencyKey == "" || o2.IdempotencyKey == "" || o1.IdempotencyKey == o2.IdempotencyKey {
		t.Errorf("IdempotencyKey들 = %q, %q — 서로 다른 비어있지 않은 값을 기대함", o1.IdempotencyKey, o2.IdempotencyKey)
	}
}
