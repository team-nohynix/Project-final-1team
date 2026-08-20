package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"orderapi/session"
)

func TestModeForOwner(t *testing.T) {
	cases := []struct {
		owner    string
		wantMode string
		wantOK   bool
	}{
		{"trader", "PAPER_TRADING", true},
		{"replayengine", "REPLAY", true},
		{"unknown", "", false},
		{"", "", false},
	}
	for _, c := range cases {
		mode, ok := modeForOwner(c.owner)
		if mode != c.wantMode || ok != c.wantOK {
			t.Errorf("modeForOwner(%q) = (%q, %v), want (%q, %v)", c.owner, mode, ok, c.wantMode, c.wantOK)
		}
	}
}

func TestCleanupUnresolvedOrdersSkipsWhenRecorderURLEmpty(t *testing.T) {
	pub := &fakePublisher{}
	record := session.RunRecord{RunID: "run_1", Owner: "replayengine", StartedAt: time.Now().Add(-time.Minute), EndedAt: time.Now()}

	cleanupUnresolvedOrders(context.Background(), &http.Client{}, "", pub, record)

	if pub.cancelCalls != 0 {
		t.Errorf("cancelCalls = %d, want 0 (recorderURL 비어있으면 아예 시도하면 안 됨)", pub.cancelCalls)
	}
}

func TestCleanupUnresolvedOrdersSkipsUnknownOwner(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	defer srv.Close()

	pub := &fakePublisher{}
	record := session.RunRecord{RunID: "run_1", Owner: "someone-else", StartedAt: time.Now()}

	cleanupUnresolvedOrders(context.Background(), srv.Client(), srv.URL, pub, record)

	if called {
		t.Error("owner를 모르면 recorder를 아예 부르면 안 됨")
	}
	if pub.cancelCalls != 0 {
		t.Errorf("cancelCalls = %d, want 0", pub.cancelCalls)
	}
}

func TestCleanupUnresolvedOrdersSkipsZeroStartedAt(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	defer srv.Close()

	pub := &fakePublisher{}
	record := session.RunRecord{RunID: "run_1", Owner: "trader"} // StartedAt zero value

	cleanupUnresolvedOrders(context.Background(), srv.Client(), srv.URL, pub, record)

	if called {
		t.Error("startedAt이 없으면 recorder를 부르면 안 됨")
	}
}

func TestCleanupUnresolvedOrdersPublishesCancelForEach(t *testing.T) {
	var gotMode, gotFrom, gotTo string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/orders/unresolved" {
			t.Errorf("path = %s", r.URL.Path)
		}
		gotMode = r.URL.Query().Get("mode")
		gotFrom = r.URL.Query().Get("from")
		gotTo = r.URL.Query().Get("to")
		json.NewEncoder(w).Encode(unresolvedOrdersResponse{Orders: []unresolvedOrder{
			{OrderID: "ord_1", Market: "KRW-BTC"},
			{OrderID: "ord_2", Market: "KRW-ETH"},
		}})
	}))
	defer srv.Close()

	pub := &fakePublisher{}
	started := time.Date(2026, 8, 19, 9, 2, 11, 0, time.UTC)
	ended := started.Add(12 * time.Minute)
	record := session.RunRecord{RunID: "run_8f2a1c", Owner: "replayengine", StartedAt: started, EndedAt: ended}

	cleanupUnresolvedOrders(context.Background(), srv.Client(), srv.URL, pub, record)

	if gotMode != "REPLAY" {
		t.Errorf("mode = %q, want REPLAY", gotMode)
	}
	if gotFrom == "" || gotTo == "" {
		t.Errorf("from/to가 비어있으면 안 됨: from=%q to=%q", gotFrom, gotTo)
	}
	if pub.cancelCalls != 2 {
		t.Errorf("cancelCalls = %d, want 2", pub.cancelCalls)
	}
	if pub.lastCanceledAt == "" {
		t.Error("lastCanceledAt이 비어있으면 안 됨")
	}
}

func TestCleanupUnresolvedOrdersEmptyResultIsNotError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(unresolvedOrdersResponse{Orders: nil})
	}))
	defer srv.Close()

	pub := &fakePublisher{}
	record := session.RunRecord{RunID: "run_1", Owner: "trader", StartedAt: time.Now().Add(-time.Minute), EndedAt: time.Now()}

	cleanupUnresolvedOrders(context.Background(), srv.Client(), srv.URL, pub, record)

	if pub.cancelCalls != 0 {
		t.Errorf("cancelCalls = %d, want 0", pub.cancelCalls)
	}
}

func TestCleanupUnresolvedOrdersRecorderErrorDoesNotPanic(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	pub := &fakePublisher{}
	record := session.RunRecord{RunID: "run_1", Owner: "trader", StartedAt: time.Now().Add(-time.Minute), EndedAt: time.Now()}

	cleanupUnresolvedOrders(context.Background(), srv.Client(), srv.URL, pub, record) // panic 안 나면 통과

	if pub.cancelCalls != 0 {
		t.Errorf("cancelCalls = %d, want 0", pub.cancelCalls)
	}
}

func TestCleanupUnresolvedOrdersEndedAtDefaultsToNowWhenZero(t *testing.T) {
	var gotTo string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotTo = r.URL.Query().Get("to")
		json.NewEncoder(w).Encode(unresolvedOrdersResponse{})
	}))
	defer srv.Close()

	pub := &fakePublisher{}
	record := session.RunRecord{RunID: "run_1", Owner: "trader", StartedAt: time.Now().Add(-time.Minute)} // EndedAt zero

	cleanupUnresolvedOrders(context.Background(), srv.Client(), srv.URL, pub, record)

	if gotTo == "" {
		t.Error("endedAt이 zero여도 to는 채워져서(지금 시각) recorder에 가야 함")
	}
}

func TestCleanupAllUnresolvedOrdersHandler(t *testing.T) {
	t.Run("recorderURL 비어있으면 503", func(t *testing.T) {
		pub := &fakePublisher{}
		w := httptest.NewRecorder()
		cleanupAllUnresolvedOrdersHandler(&http.Client{}, "", pub)(w, httptest.NewRequest(http.MethodPost, "/v1/admin/cleanup-unresolved-orders", nil))

		if w.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want %d", w.Code, http.StatusServiceUnavailable)
		}
		if pub.cancelCalls != 0 {
			t.Errorf("cancelCalls = %d, want 0", pub.cancelCalls)
		}
	})

	t.Run("recorder가 준 미종결 주문 전부 취소 발행하고 건수 응답", func(t *testing.T) {
		var gotPath string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotPath = r.URL.Path
			json.NewEncoder(w).Encode(unresolvedOrdersResponse{Orders: []unresolvedOrder{
				{OrderID: "ord_1", Market: "KRW-BTC"},
				{OrderID: "ord_2", Market: "KRW-ETH"},
				{OrderID: "ord_3", Market: "KRW-XRP"},
			}})
		}))
		defer srv.Close()

		pub := &fakePublisher{}
		w := httptest.NewRecorder()
		cleanupAllUnresolvedOrdersHandler(srv.Client(), srv.URL, pub)(w, httptest.NewRequest(http.MethodPost, "/v1/admin/cleanup-unresolved-orders", nil))

		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d, body=%s", w.Code, http.StatusOK, w.Body.String())
		}
		if gotPath != "/v1/orders/unresolved/all" {
			t.Errorf("recorder에 잘못된 경로로 요청함: %s", gotPath)
		}
		if pub.cancelCalls != 3 {
			t.Errorf("cancelCalls = %d, want 3", pub.cancelCalls)
		}
		var got map[string]int
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatalf("response not JSON: %v", err)
		}
		if got["canceled"] != 3 || got["total"] != 3 {
			t.Errorf("unexpected body: %+v", got)
		}
	})

	t.Run("recorder 조회 실패는 502", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer srv.Close()

		pub := &fakePublisher{}
		w := httptest.NewRecorder()
		cleanupAllUnresolvedOrdersHandler(srv.Client(), srv.URL, pub)(w, httptest.NewRequest(http.MethodPost, "/v1/admin/cleanup-unresolved-orders", nil))

		if w.Code != http.StatusBadGateway {
			t.Fatalf("status = %d, want %d", w.Code, http.StatusBadGateway)
		}
		if pub.cancelCalls != 0 {
			t.Errorf("cancelCalls = %d, want 0", pub.cancelCalls)
		}
	})
}
