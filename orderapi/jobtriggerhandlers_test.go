package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"orderapi/jobtrigger"
)

// fakeJobPublisher는 실제 SQS 없이 startJobHandler를 검증하기 위한
// jobtrigger.Publisher 구현체입니다 — server_test.go의 fakePublisher와 같은 패턴.
type fakeJobPublisher struct {
	mu       sync.Mutex
	calls    int
	lastReq  jobtrigger.Request
	failNext bool
}

func (f *fakeJobPublisher) Publish(ctx context.Context, req jobtrigger.Request) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failNext {
		f.failNext = false
		return context.DeadlineExceeded
	}
	f.calls++
	f.lastReq = req
	return nil
}

func TestStartJobHandler(t *testing.T) {
	t.Run("valid ai-trader request is queued", func(t *testing.T) {
		pub := &fakeJobPublisher{}
		req := httptest.NewRequest(http.MethodPost, "/v1/jobs", strings.NewReader(`{"jobType":"ai-trader","date":"2026-08-11","speed":60}`))
		w := httptest.NewRecorder()
		startJobHandler(pub, "")(w, req)

		if w.Code != http.StatusAccepted {
			t.Fatalf("status = %d, want %d", w.Code, http.StatusAccepted)
		}
		if pub.calls != 1 {
			t.Fatalf("calls = %d, want 1", pub.calls)
		}
		if pub.lastReq.JobType != "ai-trader" || pub.lastReq.Date != "2026-08-11" {
			t.Fatalf("unexpected published request: %+v", pub.lastReq)
		}
	})

	t.Run("missing orderBucket falls back to server default", func(t *testing.T) {
		pub := &fakeJobPublisher{}
		req := httptest.NewRequest(http.MethodPost, "/v1/jobs", strings.NewReader(`{"jobType":"ai-trader","date":"2026-08-11"}`))
		w := httptest.NewRecorder()
		startJobHandler(pub, "team1-truss-order-records")(w, req)

		if w.Code != http.StatusAccepted {
			t.Fatalf("status = %d, want %d", w.Code, http.StatusAccepted)
		}
		if pub.lastReq.OrderBucket != "team1-truss-order-records" {
			t.Fatalf("OrderBucket = %q, want default to be filled in", pub.lastReq.OrderBucket)
		}
	})

	t.Run("explicit orderBucket in request is not overridden", func(t *testing.T) {
		pub := &fakeJobPublisher{}
		req := httptest.NewRequest(http.MethodPost, "/v1/jobs", strings.NewReader(`{"jobType":"ai-trader","date":"2026-08-11","orderBucket":"caller-chosen-bucket"}`))
		w := httptest.NewRecorder()
		startJobHandler(pub, "team1-truss-order-records")(w, req)

		if pub.lastReq.OrderBucket != "caller-chosen-bucket" {
			t.Fatalf("OrderBucket = %q, want caller's explicit value preserved", pub.lastReq.OrderBucket)
		}
	})

	t.Run("invalid jobType is rejected before publish", func(t *testing.T) {
		pub := &fakeJobPublisher{}
		req := httptest.NewRequest(http.MethodPost, "/v1/jobs", strings.NewReader(`{"jobType":"bogus","date":"2026-08-11"}`))
		w := httptest.NewRecorder()
		startJobHandler(pub, "")(w, req)

		if w.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d", w.Code, http.StatusBadRequest)
		}
		if pub.calls != 0 {
			t.Fatalf("calls = %d, want 0 (should not publish an invalid request)", pub.calls)
		}
	})

	t.Run("malformed JSON body", func(t *testing.T) {
		pub := &fakeJobPublisher{}
		req := httptest.NewRequest(http.MethodPost, "/v1/jobs", strings.NewReader(`not json`))
		w := httptest.NewRecorder()
		startJobHandler(pub, "")(w, req)

		if w.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d", w.Code, http.StatusBadRequest)
		}
	})

	t.Run("publish failure surfaces as 500", func(t *testing.T) {
		pub := &fakeJobPublisher{failNext: true}
		req := httptest.NewRequest(http.MethodPost, "/v1/jobs", strings.NewReader(`{"jobType":"replay","date":"2026-08-11"}`))
		w := httptest.NewRecorder()
		startJobHandler(pub, "")(w, req)

		if w.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want %d", w.Code, http.StatusInternalServerError)
		}

		var body map[string]string
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatalf("response body not JSON: %v", err)
		}
		if body["errorCode"] != "INTERNAL_ERROR" {
			t.Fatalf("errorCode = %q, want INTERNAL_ERROR", body["errorCode"])
		}
	})
}
