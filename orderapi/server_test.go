package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"orderapi/idempotency"
	"orderapi/kafkaclient"
	"orderapi/order"
)

// fakePublisher는 실제 Kafka 없이 핸들러를 검증하기 위한 kafkaclient.Publisher 구현체입니다.
type fakePublisher struct {
	mu                  sync.Mutex
	newCalls            int
	cancelCalls         int
	failNext            bool
	lastClientRequestID string
	lastMode            string
	lastCanceledAt      string
	lastSourceOrderID   string
}

func (f *fakePublisher) PublishNew(ctx context.Context, o *order.Order, clientRequestID, mode, sourceOrderID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failNext {
		f.failNext = false
		return context.DeadlineExceeded
	}
	f.newCalls++
	f.lastClientRequestID = clientRequestID
	f.lastMode = mode
	f.lastSourceOrderID = sourceOrderID
	return nil
}

func (f *fakePublisher) PublishCancel(ctx context.Context, orderID, market, canceledAt string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancelCalls++
	f.lastCanceledAt = canceledAt
	return nil
}

func newOrderMux(store *order.Store, idem *idempotency.Store, pub kafkaclient.Publisher) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/orders", acceptOrderHandler(store, idem, pub))
	mux.HandleFunc("DELETE /v1/orders/{orderId}", cancelOrderHandler(store, pub))
	return mux
}

func postOrder(mux *http.ServeMux, idempotencyKey, body string) *httptest.ResponseRecorder {
	return postOrderWithMode(mux, idempotencyKey, "", body)
}

func postOrderWithMode(mux *http.ServeMux, idempotencyKey, mode, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/v1/orders", strings.NewReader(body))
	if idempotencyKey != "" {
		req.Header.Set("Idempotency-Key", idempotencyKey)
	}
	if mode != "" {
		req.Header.Set("X-Order-Mode", mode)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestAcceptOrderMissingIdempotencyKey(t *testing.T) {
	mux := newOrderMux(order.NewStore(), idempotency.NewStore(), &fakePublisher{})
	rec := postOrder(mux, "", `{"market":"KRW-BTC","side":"BUY","price":"71500000","quantity":"0.015"}`)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	var got errorResponse
	json.NewDecoder(rec.Body).Decode(&got)
	if got.ErrorCode != "MISSING_IDEMPOTENCY_KEY" {
		t.Errorf("errorCode = %q, want MISSING_IDEMPOTENCY_KEY", got.ErrorCode)
	}
}

func TestAcceptOrderInvalidMarket(t *testing.T) {
	mux := newOrderMux(order.NewStore(), idempotency.NewStore(), &fakePublisher{})
	rec := postOrder(mux, "key-1", `{"market":"KRW-NOTREAL","side":"BUY","price":"100","quantity":"1"}`)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	var got errorResponse
	json.NewDecoder(rec.Body).Decode(&got)
	if got.ErrorCode != "INVALID_MARKET" {
		t.Errorf("errorCode = %q, want INVALID_MARKET", got.ErrorCode)
	}
}

func TestAcceptOrderSuccess(t *testing.T) {
	pub := &fakePublisher{}
	mux := newOrderMux(order.NewStore(), idempotency.NewStore(), pub)
	rec := postOrder(mux, "key-1", `{"market":"KRW-BTC","side":"BUY","price":"71500000","quantity":"0.015"}`)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202, body=%s", rec.Code, rec.Body.String())
	}
	var got order.Order
	json.NewDecoder(rec.Body).Decode(&got)
	if got.Status != order.StatusAccepted || got.Market != "KRW-BTC" || got.OrderID == "" {
		t.Errorf("got = %+v", got)
	}
	if pub.newCalls != 1 {
		t.Errorf("PublishNew 호출 횟수 = %d, want 1", pub.newCalls)
	}
	if pub.lastClientRequestID != "key-1" {
		t.Errorf("PublishNew에 전달된 clientRequestID = %q, want %q (Idempotency-Key가 그대로 전달돼야 함)", pub.lastClientRequestID, "key-1")
	}
}

func TestAcceptOrderModeHeaderPassedThrough(t *testing.T) {
	pub := &fakePublisher{}
	mux := newOrderMux(order.NewStore(), idempotency.NewStore(), pub)
	postOrderWithMode(mux, "key-replay", "REPLAY", `{"market":"KRW-BTC","side":"BUY","price":"71500000","quantity":"0.015"}`)

	if pub.lastMode != "REPLAY" {
		t.Errorf("PublishNew에 전달된 mode = %q, want REPLAY", pub.lastMode)
	}
}

func TestAcceptOrderSourceOrderIDPassedThrough(t *testing.T) {
	pub := &fakePublisher{}
	mux := newOrderMux(order.NewStore(), idempotency.NewStore(), pub)
	postOrder(mux, "key-1", `{"market":"KRW-BTC","side":"BUY","price":"71500000","quantity":"0.015","sourceOrderId":"ord_20260806_0000001"}`)

	if pub.lastSourceOrderID != "ord_20260806_0000001" {
		t.Errorf("PublishNew에 전달된 sourceOrderID = %q, want ord_20260806_0000001", pub.lastSourceOrderID)
	}
}

func TestAcceptOrderModeHeaderMissingDefaultsToPaperTrading(t *testing.T) {
	pub := &fakePublisher{}
	mux := newOrderMux(order.NewStore(), idempotency.NewStore(), pub)
	// X-Order-Mode 헤더를 아예 안 보냄 — 기존 curl/수동 테스트 워크플로를 깨면 안 됨.
	postOrder(mux, "key-1", `{"market":"KRW-BTC","side":"BUY","price":"71500000","quantity":"0.015"}`)

	if pub.lastMode != "PAPER_TRADING" {
		t.Errorf("mode 헤더가 없으면 PAPER_TRADING으로 기본 처리돼야 하는데 %q", pub.lastMode)
	}
}

func TestAcceptOrderModeHeaderInvalidDefaultsToPaperTrading(t *testing.T) {
	pub := &fakePublisher{}
	mux := newOrderMux(order.NewStore(), idempotency.NewStore(), pub)
	postOrderWithMode(mux, "key-1", "NOT_A_REAL_MODE", `{"market":"KRW-BTC","side":"BUY","price":"71500000","quantity":"0.015"}`)

	if pub.lastMode != "PAPER_TRADING" {
		t.Errorf("알 수 없는 mode 값은 PAPER_TRADING으로 기본 처리돼야 하는데 %q", pub.lastMode)
	}
}

func TestAcceptOrderIdempotentRetryReturnsSameResponseWithoutRepublishing(t *testing.T) {
	pub := &fakePublisher{}
	mux := newOrderMux(order.NewStore(), idempotency.NewStore(), pub)
	body := `{"market":"KRW-BTC","side":"BUY","price":"71500000","quantity":"0.015"}`

	first := postOrder(mux, "same-key", body)
	second := postOrder(mux, "same-key", body)

	if first.Body.String() != second.Body.String() {
		t.Errorf("같은 Idempotency-Key 재요청 응답이 달라짐:\n1차: %s\n2차: %s", first.Body.String(), second.Body.String())
	}
	if pub.newCalls != 1 {
		t.Errorf("재요청 시 재발행되면 안 되는데 PublishNew 호출 횟수 = %d", pub.newCalls)
	}
}

func TestAcceptOrderPublishFailureNotCachedAndAllowsRetry(t *testing.T) {
	pub := &fakePublisher{failNext: true}
	idem := idempotency.NewStore()
	mux := newOrderMux(order.NewStore(), idem, pub)
	body := `{"market":"KRW-BTC","side":"BUY","price":"71500000","quantity":"0.015"}`

	first := postOrder(mux, "retry-key", body)
	if first.Code != http.StatusInternalServerError {
		t.Fatalf("첫 시도는 발행 실패로 500이어야 하는데 %d", first.Code)
	}

	second := postOrder(mux, "retry-key", body)
	if second.Code != http.StatusAccepted {
		t.Fatalf("발행 실패는 캐싱되지 않아 재시도가 성공해야 하는데 %d, body=%s", second.Code, second.Body.String())
	}
}

func TestCancelOrderNotFound(t *testing.T) {
	mux := newOrderMux(order.NewStore(), idempotency.NewStore(), &fakePublisher{})
	req := httptest.NewRequest(http.MethodDelete, "/v1/orders/ord_없음", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestCancelOrderSuccessThenIdempotentNoOp(t *testing.T) {
	store := order.NewStore()
	store.Save(&order.Order{OrderID: "ord_1", Market: "KRW-BTC", Quantity: "0.015", Status: order.StatusAccepted})
	pub := &fakePublisher{}
	mux := newOrderMux(store, idempotency.NewStore(), pub)

	req := httptest.NewRequest(http.MethodDelete, "/v1/orders/ord_1", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", rec.Code, rec.Body.String())
	}
	var got cancelResponse
	json.NewDecoder(rec.Body).Decode(&got)
	if got.Status != order.StatusCanceled || got.CanceledQuantity != "0.015" {
		t.Errorf("got = %+v", got)
	}
	firstCanceledAt := got.CanceledAt

	// 같은 주문을 다시 취소 요청 — 재처리 없이 같은 결과가 나와야 함.
	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, httptest.NewRequest(http.MethodDelete, "/v1/orders/ord_1", nil))
	var got2 cancelResponse
	json.NewDecoder(rec2.Body).Decode(&got2)
	if got2.CanceledAt != firstCanceledAt {
		t.Errorf("이미 취소된 주문을 다시 취소하면 같은 canceledAt을 유지해야 하는데 %q != %q", got2.CanceledAt, firstCanceledAt)
	}
	if pub.cancelCalls != 1 {
		t.Errorf("두 번째 취소 요청은 재발행하면 안 되는데 PublishCancel 호출 횟수 = %d", pub.cancelCalls)
	}
	if pub.lastCanceledAt != firstCanceledAt {
		t.Errorf("PublishCancel에 전달된 canceledAt = %q, want %q (응답의 canceledAt과 같아야 함)", pub.lastCanceledAt, firstCanceledAt)
	}
}

func TestCancelOrderAlreadyFilled(t *testing.T) {
	store := order.NewStore()
	store.Save(&order.Order{OrderID: "ord_1", Market: "KRW-BTC", Status: order.StatusFilled})
	mux := newOrderMux(store, idempotency.NewStore(), &fakePublisher{})

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/v1/orders/ord_1", nil))

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
	var got errorResponse
	json.NewDecoder(rec.Body).Decode(&got)
	if got.ErrorCode != "ORDER_ALREADY_FILLED" {
		t.Errorf("errorCode = %q, want ORDER_ALREADY_FILLED", got.ErrorCode)
	}
}
