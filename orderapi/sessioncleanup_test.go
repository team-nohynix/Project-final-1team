package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// resetCleanupAllStatus는 cleanupAllLatest(패키지 전역, sessioncleanup.go
// 참고)를 테스트마다 깨끗한 상태로 되돌립니다 — 이 전역 상태를 여러 테스트가
// 공유하므로 필요합니다.
func resetCleanupAllStatus(t *testing.T) {
	t.Helper()
	cleanupAllMu.Lock()
	cleanupAllLatest = cleanupAllStatus{}
	cleanupAllMu.Unlock()
}

func TestCleanupUnresolvedOrdersSkipsWhenRecorderURLEmpty(t *testing.T) {
	pub := &fakePublisher{}

	cleanupUnresolvedOrders(context.Background(), &http.Client{}, "", pub, "run_1", time.Now().Add(-time.Minute))

	if pub.cancelCalls != 0 {
		t.Errorf("cancelCalls = %d, want 0 (recorderURL 비어있으면 아예 시도하면 안 됨)", pub.cancelCalls)
	}
}

func TestCleanupUnresolvedOrdersSkipsZeroStartedAt(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	defer srv.Close()

	pub := &fakePublisher{}
	cleanupUnresolvedOrders(context.Background(), srv.Client(), srv.URL, pub, "run_1", time.Time{}) // startedAt zero value

	if called {
		t.Error("startedAt이 없으면(RunRecord가 비정상) recorder를 부르면 안 됨")
	}
}

// TestCleanupUnresolvedOrdersPublishesCancelForEach는 2026-08-20부터 범위가
// "이 세션 몫만"에서 "전체 미종결"로 넓어진 걸 확인합니다 — mode/from/to
// 쿼리 파라미터 없이 GET /v1/orders/unresolved/all을 부르는지가 핵심.
func TestCleanupUnresolvedOrdersPublishesCancelForEach(t *testing.T) {
	var gotPath, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		json.NewEncoder(w).Encode(unresolvedOrdersResponse{Orders: []unresolvedOrder{
			{OrderID: "ord_1", Market: "KRW-BTC"},
			{OrderID: "ord_2", Market: "KRW-ETH"},
		}})
	}))
	defer srv.Close()

	pub := &fakePublisher{}
	started := time.Date(2026, 8, 19, 9, 2, 11, 0, time.UTC)

	cleanupUnresolvedOrders(context.Background(), srv.Client(), srv.URL, pub, "run_8f2a1c", started)

	if gotPath != "/v1/orders/unresolved/all" {
		t.Errorf("path = %q, want /v1/orders/unresolved/all (범위 제한 없는 조회여야 함)", gotPath)
	}
	if gotQuery != "" {
		t.Errorf("query = %q, want empty (mode/from/to로 더 이상 좁히면 안 됨)", gotQuery)
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
	cleanupUnresolvedOrders(context.Background(), srv.Client(), srv.URL, pub, "run_1", time.Now().Add(-time.Minute))

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
	cleanupUnresolvedOrders(context.Background(), srv.Client(), srv.URL, pub, "run_1", time.Now().Add(-time.Minute)) // panic 안 나면 통과

	if pub.cancelCalls != 0 {
		t.Errorf("cancelCalls = %d, want 0", pub.cancelCalls)
	}
}

func TestStartCleanupAllUnresolvedOrdersHandlerRecorderURLEmpty(t *testing.T) {
	resetCleanupAllStatus(t)
	pub := &fakePublisher{}
	w := httptest.NewRecorder()
	startCleanupAllUnresolvedOrdersHandler(&http.Client{}, "", pub)(w, httptest.NewRequest(http.MethodPost, "/v1/admin/cleanup-unresolved-orders", nil))

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusServiceUnavailable)
	}
	if pub.cancelCalls != 0 {
		t.Errorf("cancelCalls = %d, want 0", pub.cancelCalls)
	}
}

// TestStartCleanupAllUnresolvedOrdersHandlerReturnsImmediately는 2026-08-20의
// 핵심 변경사항입니다 — recorder 응답이 느려도(여기선 채널로 인위적으로
// 블록) POST 핸들러 자체는 즉시 202로 응답해야 합니다(더 이상 동기 처리
// 아님).
func TestStartCleanupAllUnresolvedOrdersHandlerReturnsImmediately(t *testing.T) {
	resetCleanupAllStatus(t)
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-block // 핸들러가 이 응답을 기다리지 않는지 확인하기 위해 일부러 블록
		json.NewEncoder(w).Encode(unresolvedOrdersResponse{})
	}))
	defer func() {
		close(block)
		srv.Close()
	}()

	pub := &fakePublisher{}
	w := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		startCleanupAllUnresolvedOrdersHandler(srv.Client(), srv.URL, pub)(w, httptest.NewRequest(http.MethodPost, "/v1/admin/cleanup-unresolved-orders", nil))
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("핸들러가 recorder 응답(블록 중)을 기다린 것으로 보임 — 비동기가 아님")
	}

	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d, body=%s", w.Code, http.StatusAccepted, w.Body.String())
	}
	var got cleanupAllStatus
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("응답 파싱 실패: %v", err)
	}
	if got.Status != "IN_PROGRESS" {
		t.Errorf("status = %q, want IN_PROGRESS", got.Status)
	}
}

func TestStartCleanupAllUnresolvedOrdersHandlerConflictWhileInProgress(t *testing.T) {
	resetCleanupAllStatus(t)
	cleanupAllMu.Lock()
	cleanupAllLatest = cleanupAllStatus{Status: "IN_PROGRESS", StartedAt: time.Now().UTC().Format(time.RFC3339)}
	cleanupAllMu.Unlock()

	pub := &fakePublisher{}
	w := httptest.NewRecorder()
	startCleanupAllUnresolvedOrdersHandler(&http.Client{}, "http://unused", pub)(w, httptest.NewRequest(http.MethodPost, "/v1/admin/cleanup-unresolved-orders", nil))

	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusConflict)
	}
}

// TestRunCleanupAllUnresolvedOrders는 백그라운드 작업 본체를 직접(동기적으로)
// 호출해 로직만 검증합니다 — 고루틴 타이밍에 의존하지 않기 위함.
func TestRunCleanupAllUnresolvedOrders(t *testing.T) {
	resetCleanupAllStatus(t)
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
	runCleanupAllUnresolvedOrders(context.Background(), srv.Client(), srv.URL, pub)

	if gotPath != "/v1/orders/unresolved/all" {
		t.Errorf("recorder에 잘못된 경로로 요청함: %s", gotPath)
	}
	if pub.cancelCalls != 3 {
		t.Errorf("cancelCalls = %d, want 3", pub.cancelCalls)
	}

	cleanupAllMu.Lock()
	got := cleanupAllLatest
	cleanupAllMu.Unlock()
	if got.Status != "COMPLETED" || got.Canceled != 3 || got.Total != 3 || got.EndedAt == "" {
		t.Errorf("unexpected final status: %+v", got)
	}
}

func TestRunCleanupAllUnresolvedOrdersRecorderErrorMarksFailed(t *testing.T) {
	resetCleanupAllStatus(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	pub := &fakePublisher{}
	runCleanupAllUnresolvedOrders(context.Background(), srv.Client(), srv.URL, pub)

	if pub.cancelCalls != 0 {
		t.Errorf("cancelCalls = %d, want 0", pub.cancelCalls)
	}
	cleanupAllMu.Lock()
	got := cleanupAllLatest
	cleanupAllMu.Unlock()
	if got.Status != "FAILED" || got.Message == "" {
		t.Errorf("unexpected final status: %+v", got)
	}
}

func TestCleanupAllStatusHandlerNoneYet(t *testing.T) {
	resetCleanupAllStatus(t)
	w := httptest.NewRecorder()
	cleanupAllStatusHandler()(w, httptest.NewRequest(http.MethodGet, "/v1/admin/cleanup-unresolved-orders/status", nil))

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusNotFound)
	}
}

func TestCleanupAllStatusHandlerReturnsLatest(t *testing.T) {
	resetCleanupAllStatus(t)
	cleanupAllMu.Lock()
	cleanupAllLatest = cleanupAllStatus{Status: "COMPLETED", Canceled: 5, Total: 5, StartedAt: "2026-08-20T00:00:00Z", EndedAt: "2026-08-20T00:01:00Z"}
	cleanupAllMu.Unlock()

	w := httptest.NewRecorder()
	cleanupAllStatusHandler()(w, httptest.NewRequest(http.MethodGet, "/v1/admin/cleanup-unresolved-orders/status", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
	var got cleanupAllStatus
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("응답 파싱 실패: %v", err)
	}
	if got.Status != "COMPLETED" || got.Canceled != 5 || got.Total != 5 {
		t.Errorf("unexpected body: %+v", got)
	}
}
