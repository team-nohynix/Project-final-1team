package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"recorder/query"
)

// fakeQuerier는 실제 MySQL 없이 핸들러를 검증하기 위한 query.Querier
// 구현체입니다 — orderapi/server_test.go의 fakePublisher와 같은 패턴.
type fakeQuerier struct {
	trace         query.OrderTrace
	traceFound    bool
	traceErr      error
	engines       []query.EngineAssignment
	enginesErr    error
	metrics       query.DashboardMetrics
	metricsErr    error
	summary       query.OrderSummary
	summaryErr    error
	unresolved    []query.UnresolvedOrder
	unresolvedErr error
	gotMode       string
	gotFrom       time.Time
	gotTo         time.Time
}

func (f *fakeQuerier) TraceOrder(ctx context.Context, orderID string) (query.OrderTrace, bool, error) {
	return f.trace, f.traceFound, f.traceErr
}

func (f *fakeQuerier) ListEngines(ctx context.Context) ([]query.EngineAssignment, error) {
	return f.engines, f.enginesErr
}

func (f *fakeQuerier) DashboardMetrics(ctx context.Context) (query.DashboardMetrics, error) {
	return f.metrics, f.metricsErr
}

func (f *fakeQuerier) OrderSummary(ctx context.Context, mode string, from, to time.Time) (query.OrderSummary, error) {
	f.gotMode, f.gotFrom, f.gotTo = mode, from, to
	return f.summary, f.summaryErr
}

func (f *fakeQuerier) UnresolvedOrders(ctx context.Context, mode string, from, to time.Time) ([]query.UnresolvedOrder, error) {
	f.gotMode, f.gotFrom, f.gotTo = mode, from, to
	return f.unresolved, f.unresolvedErr
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

func TestDashboardMetricsHandler(t *testing.T) {
	t.Run("returns metrics", func(t *testing.T) {
		q := &fakeQuerier{metrics: query.DashboardMetrics{
			OrderAcceptTps:    12.3,
			ExecutionTps:      8.7,
			PendingOrders:     42,
			E2EP99Ms:          1230.5,
			E2EP99SampleCount: 57,
			RunningEnginePods: 1,
			Series:            []query.MetricsBucket{{BucketStart: "2026-08-12T06:10:00Z", Orders: 120, Executions: 95}},
		}}
		w := httptest.NewRecorder()
		dashboardMetricsHandler(q)(w, httptest.NewRequest(http.MethodGet, "/v1/metrics/dashboard", nil))

		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
		}
		var got query.DashboardMetrics
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatalf("response not JSON: %v", err)
		}
		if got.PendingOrders != 42 || got.RunningEnginePods != 1 || len(got.Series) != 1 {
			t.Fatalf("unexpected metrics: %+v", got)
		}
	})

	t.Run("query error", func(t *testing.T) {
		q := &fakeQuerier{metricsErr: context.DeadlineExceeded}
		w := httptest.NewRecorder()
		dashboardMetricsHandler(q)(w, httptest.NewRequest(http.MethodGet, "/v1/metrics/dashboard", nil))

		if w.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want %d", w.Code, http.StatusInternalServerError)
		}
	})
}

func TestOrderSummaryHandler(t *testing.T) {
	t.Run("missing mode", func(t *testing.T) {
		q := &fakeQuerier{}
		w := httptest.NewRecorder()
		orderSummaryHandler(q)(w, httptest.NewRequest(http.MethodGet, "/v1/orders/summary?from=2026-08-13T00:00:00Z", nil))

		if w.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d", w.Code, http.StatusBadRequest)
		}
	})

	t.Run("invalid mode", func(t *testing.T) {
		q := &fakeQuerier{}
		w := httptest.NewRecorder()
		orderSummaryHandler(q)(w, httptest.NewRequest(http.MethodGet, "/v1/orders/summary?mode=BOGUS&from=2026-08-13T00:00:00Z", nil))

		if w.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d", w.Code, http.StatusBadRequest)
		}
	})

	t.Run("missing from", func(t *testing.T) {
		q := &fakeQuerier{}
		w := httptest.NewRecorder()
		orderSummaryHandler(q)(w, httptest.NewRequest(http.MethodGet, "/v1/orders/summary?mode=PAPER_TRADING", nil))

		if w.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d", w.Code, http.StatusBadRequest)
		}
	})

	t.Run("invalid from", func(t *testing.T) {
		q := &fakeQuerier{}
		w := httptest.NewRecorder()
		orderSummaryHandler(q)(w, httptest.NewRequest(http.MethodGet, "/v1/orders/summary?mode=PAPER_TRADING&from=not-a-time", nil))

		if w.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d", w.Code, http.StatusBadRequest)
		}
	})

	t.Run("invalid to", func(t *testing.T) {
		q := &fakeQuerier{}
		w := httptest.NewRecorder()
		url := "/v1/orders/summary?mode=PAPER_TRADING&from=2026-08-13T00:00:00Z&to=not-a-time"
		orderSummaryHandler(q)(w, httptest.NewRequest(http.MethodGet, url, nil))

		if w.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d", w.Code, http.StatusBadRequest)
		}
	})

	t.Run("to omitted defaults to zero value (querier decides 'now')", func(t *testing.T) {
		q := &fakeQuerier{summary: query.OrderSummary{Accepted: 10, Filled: 7, Unfilled: 3}}
		w := httptest.NewRecorder()
		orderSummaryHandler(q)(w, httptest.NewRequest(http.MethodGet, "/v1/orders/summary?mode=PAPER_TRADING&from=2026-08-13T00:00:00Z", nil))

		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
		}
		if q.gotMode != "PAPER_TRADING" || !q.gotTo.IsZero() {
			t.Fatalf("unexpected args passed to querier: mode=%s to=%v", q.gotMode, q.gotTo)
		}
		var got query.OrderSummary
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatalf("response not JSON: %v", err)
		}
		if got.Accepted != 10 || got.Filled != 7 || got.Unfilled != 3 {
			t.Fatalf("unexpected summary: %+v", got)
		}
	})

	t.Run("mode/from/to all passed through", func(t *testing.T) {
		q := &fakeQuerier{summary: query.OrderSummary{Accepted: 1}}
		w := httptest.NewRecorder()
		url := "/v1/orders/summary?mode=REPLAY&from=2026-08-13T00:00:00Z&to=2026-08-13T01:00:00Z"
		orderSummaryHandler(q)(w, httptest.NewRequest(http.MethodGet, url, nil))

		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
		}
		if q.gotMode != "REPLAY" {
			t.Fatalf("mode = %s, want REPLAY", q.gotMode)
		}
		if q.gotFrom.IsZero() || q.gotTo.IsZero() {
			t.Fatalf("from/to should not be zero: from=%v to=%v", q.gotFrom, q.gotTo)
		}
	})

	t.Run("query error", func(t *testing.T) {
		q := &fakeQuerier{summaryErr: context.DeadlineExceeded}
		w := httptest.NewRecorder()
		orderSummaryHandler(q)(w, httptest.NewRequest(http.MethodGet, "/v1/orders/summary?mode=PAPER_TRADING&from=2026-08-13T00:00:00Z", nil))

		if w.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want %d", w.Code, http.StatusInternalServerError)
		}
	})
}

func TestUnresolvedOrdersHandler(t *testing.T) {
	t.Run("missing mode", func(t *testing.T) {
		q := &fakeQuerier{}
		w := httptest.NewRecorder()
		unresolvedOrdersHandler(q)(w, httptest.NewRequest(http.MethodGet, "/v1/orders/unresolved?from=2026-08-19T00:00:00Z", nil))

		if w.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d", w.Code, http.StatusBadRequest)
		}
	})

	t.Run("missing from", func(t *testing.T) {
		q := &fakeQuerier{}
		w := httptest.NewRecorder()
		unresolvedOrdersHandler(q)(w, httptest.NewRequest(http.MethodGet, "/v1/orders/unresolved?mode=REPLAY", nil))

		if w.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d", w.Code, http.StatusBadRequest)
		}
	})

	t.Run("returns unresolved orders", func(t *testing.T) {
		q := &fakeQuerier{unresolved: []query.UnresolvedOrder{
			{OrderID: "ord_1", Market: "KRW-BTC"},
			{OrderID: "ord_2", Market: "KRW-ETH"},
		}}
		w := httptest.NewRecorder()
		url := "/v1/orders/unresolved?mode=REPLAY&from=2026-08-19T09:02:11Z&to=2026-08-19T09:14:37Z"
		unresolvedOrdersHandler(q)(w, httptest.NewRequest(http.MethodGet, url, nil))

		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d, body=%s", w.Code, http.StatusOK, w.Body.String())
		}
		if q.gotMode != "REPLAY" || q.gotFrom.IsZero() || q.gotTo.IsZero() {
			t.Fatalf("unexpected args passed to querier: mode=%s from=%v to=%v", q.gotMode, q.gotFrom, q.gotTo)
		}
		var got map[string][]query.UnresolvedOrder
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatalf("response not JSON: %v", err)
		}
		if len(got["orders"]) != 2 || got["orders"][0].OrderID != "ord_1" {
			t.Fatalf("unexpected orders: %+v", got)
		}
	})

	t.Run("empty result is not an error", func(t *testing.T) {
		q := &fakeQuerier{unresolved: nil}
		w := httptest.NewRecorder()
		unresolvedOrdersHandler(q)(w, httptest.NewRequest(http.MethodGet, "/v1/orders/unresolved?mode=PAPER_TRADING&from=2026-08-19T00:00:00Z", nil))

		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
		}
	})

	t.Run("query error", func(t *testing.T) {
		q := &fakeQuerier{unresolvedErr: context.DeadlineExceeded}
		w := httptest.NewRecorder()
		unresolvedOrdersHandler(q)(w, httptest.NewRequest(http.MethodGet, "/v1/orders/unresolved?mode=REPLAY&from=2026-08-19T00:00:00Z", nil))

		if w.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want %d", w.Code, http.StatusInternalServerError)
		}
	})
}
