package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"recorder/query"
)

// fakeQuerier는 실제 MySQL 없이 핸들러를 검증하기 위한 query.Querier
// 구현체입니다 — orderapi/server_test.go의 fakePublisher와 같은 패턴.
type fakeQuerier struct {
	trace      query.OrderTrace
	traceFound bool
	traceErr   error
	engines    []query.EngineAssignment
	enginesErr error
}

func (f *fakeQuerier) TraceOrder(ctx context.Context, orderID string) (query.OrderTrace, bool, error) {
	return f.trace, f.traceFound, f.traceErr
}

func (f *fakeQuerier) ListEngines(ctx context.Context) ([]query.EngineAssignment, error) {
	return f.engines, f.enginesErr
}

func newTraceRequest(orderID string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/v1/trace/"+orderID, nil)
	req.SetPathValue("orderId", orderID)
	return req
}

func TestTraceHandler(t *testing.T) {
	t.Run("found", func(t *testing.T) {
		q := &fakeQuerier{
			traceFound: true,
			trace: query.OrderTrace{
				OrderID: "ord_1", Market: "KRW-BTC", Status: "FILLED",
				Executions: []query.ExecutionTrace{{ExecutionID: "exec_1"}},
			},
		}
		w := httptest.NewRecorder()
		traceHandler(q)(w, newTraceRequest("ord_1"))

		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
		}
		var got query.OrderTrace
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatalf("response not JSON: %v", err)
		}
		if got.OrderID != "ord_1" || len(got.Executions) != 1 {
			t.Fatalf("unexpected trace: %+v", got)
		}
	})

	t.Run("not found", func(t *testing.T) {
		q := &fakeQuerier{traceFound: false}
		w := httptest.NewRecorder()
		traceHandler(q)(w, newTraceRequest("ord_missing"))

		if w.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want %d", w.Code, http.StatusNotFound)
		}
	})

	t.Run("query error", func(t *testing.T) {
		q := &fakeQuerier{traceErr: context.DeadlineExceeded}
		w := httptest.NewRecorder()
		traceHandler(q)(w, newTraceRequest("ord_1"))

		if w.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want %d", w.Code, http.StatusInternalServerError)
		}
	})
}

func TestEnginesHandler(t *testing.T) {
	t.Run("returns engines", func(t *testing.T) {
		q := &fakeQuerier{engines: []query.EngineAssignment{
			{EngineInstanceID: "engine_1", Markets: []query.MarketAssignment{{Market: "KRW-BTC"}}},
		}}
		w := httptest.NewRecorder()
		enginesHandler(q)(w, httptest.NewRequest(http.MethodGet, "/v1/matching/engines", nil))

		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
		}
		var got map[string][]query.EngineAssignment
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatalf("response not JSON: %v", err)
		}
		if len(got["engines"]) != 1 || got["engines"][0].EngineInstanceID != "engine_1" {
			t.Fatalf("unexpected engines: %+v", got)
		}
	})

	t.Run("query error", func(t *testing.T) {
		q := &fakeQuerier{enginesErr: context.DeadlineExceeded}
		w := httptest.NewRecorder()
		enginesHandler(q)(w, httptest.NewRequest(http.MethodGet, "/v1/matching/engines", nil))

		if w.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want %d", w.Code, http.StatusInternalServerError)
		}
	})
}
