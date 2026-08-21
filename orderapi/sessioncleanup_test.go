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

// waitForCleanupAllSettled는 startCleanupAllUnresolvedOrdersHandler가 띄운
// 백그라운드 고루틴(runCleanupAllUnresolvedOrders)이 IN_PROGRESS를 벗어날
// 때까지 기다립니다 — 이 고루틴이 테스트 함수가 끝난 뒤까지 살아있으면
// cleanupAllLatest(패키지 전역)를 뒤늦게 덮어써서 다음 테스트를 흔드는
// flaky 실패로 이어집니다(실측: CI에서 이 이유로
// TestStartCleanupAllUnresolvedOrdersHandlerConflictWhileInProgress가 깨짐).
// 202를 응답한 뒤 고루틴을 스폰하는 테스트는 반환 전에 반드시 이 함수를
// 불러야 합니다.
func waitForCleanupAllSettled(t *testing.T) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		cleanupAllMu.Lock()
		status := cleanupAllLatest.Status
		cleanupAllMu.Unlock()
		if status != "IN_PROGRESS" {
			return
		}
		select {
		case <-deadline:
			t.Fatal("백그라운드 정리 고루틴이 제한 시간 안에 끝나지 않음")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestCleanupUnresolvedOrdersSkipsWhenRecorderURLEmpty(t *testing.T) {
	pub := &fakePublisher{}

	cleanupUnresolvedOrders(context.Background(), &http.Client{}, "", pub, &fakeSessionStore{}, &fakeChecker{}, "run_1", time.Now().Add(-time.Minute))

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
	cleanupUnresolvedOrders(context.Background(), srv.Client(), srv.URL, pub, &fakeSessionStore{}, &fakeChecker{}, "run_1", time.Time{}) // startedAt zero value

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

	cleanupUnresolvedOrders(context.Background(), srv.Client(), srv.URL, pub, &fakeSessionStore{}, &fakeChecker{}, "run_8f2a1c", started)

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
	cleanupUnresolvedOrders(context.Background(), srv.Client(), srv.URL, pub, &fakeSessionStore{}, &fakeChecker{}, "run_1", time.Now().Add(-time.Minute))

	if pub.cancelCalls != 0 {
		t.Errorf("cancelCalls = %d, want 0", pub.cancelCalls)
	}
}

// TestCleanupUnresolvedOrdersSkipsAndLeavesNoteWhenMatchingLagging는
// 2026-08-20 추가 — 매칭 엔진이 랙 상태면 recorder를 아예 안 부르고
// 건너뛰면서, "실행 결과" 화면에서 보이도록 RunRecord.Message에 안내
// 문구를 남기는지 확인합니다(재시도 루프는 일부러 안 둠 — 다음 세션
// 종료 때 다시 시도되는 구조에 기댐).
func TestCleanupUnresolvedOrdersSkipsAndLeavesNoteWhenMatchingLagging(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	defer srv.Close()

	pub := &fakePublisher{}
	store := &fakeSessionStore{}
	cleanupUnresolvedOrders(context.Background(), srv.Client(), srv.URL, pub, store, &fakeChecker{active: true}, "run_1", time.Now().Add(-time.Minute))

	if called {
		t.Error("매칭 엔진이 랙 상태면 recorder를 부르면 안 됨")
	}
	if pub.cancelCalls != 0 {
		t.Errorf("cancelCalls = %d, want 0", pub.cancelCalls)
	}
	if store.lastNoteRunID != "run_1" || store.lastNote == "" {
		t.Errorf("실행 결과 메시지에 안내가 안 남음: runId=%q, note=%q", store.lastNoteRunID, store.lastNote)
	}
}

// TestCleanupUnresolvedOrdersLagCheckErrorFailsOpen은 랙 확인 자체가
// 실패해도(Redis 순간 장애 등) 정리를 계속 진행하는지 확인합니다.
func TestCleanupUnresolvedOrdersLagCheckErrorFailsOpen(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(unresolvedOrdersResponse{Orders: []unresolvedOrder{{OrderID: "ord_1", Market: "KRW-BTC"}}})
	}))
	defer srv.Close()

	pub := &fakePublisher{}
	cleanupUnresolvedOrders(context.Background(), srv.Client(), srv.URL, pub, &fakeSessionStore{}, &fakeChecker{err: context.DeadlineExceeded}, "run_1", time.Now().Add(-time.Minute))

	if pub.cancelCalls != 1 {
		t.Errorf("cancelCalls = %d, want 1 (랙 확인 실패는 fail-open이어야 함)", pub.cancelCalls)
	}
}

func TestCleanupUnresolvedOrdersRecorderErrorDoesNotPanic(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	pub := &fakePublisher{}
	cleanupUnresolvedOrders(context.Background(), srv.Client(), srv.URL, pub, &fakeSessionStore{}, &fakeChecker{}, "run_1", time.Now().Add(-time.Minute)) // panic 안 나면 통과

	if pub.cancelCalls != 0 {
		t.Errorf("cancelCalls = %d, want 0", pub.cancelCalls)
	}
}

func TestStartCleanupAllUnresolvedOrdersHandlerRecorderURLEmpty(t *testing.T) {
	resetCleanupAllStatus(t)
	pub := &fakePublisher{}
	w := httptest.NewRecorder()
	startCleanupAllUnresolvedOrdersHandler(&http.Client{}, "", pub, &fakeChecker{})(w, httptest.NewRequest(http.MethodPost, "/v1/admin/cleanup-unresolved-orders", nil))

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusServiceUnavailable)
	}
	if pub.cancelCalls != 0 {
		t.Errorf("cancelCalls = %d, want 0", pub.cancelCalls)
	}
}

// TestStartCleanupAllUnresolvedOrdersHandlerBlocksWhenMatchingLagging는
// 2026-08-20 추가 — 매칭 엔진이 이미 컨슈머 랙 백프레셔 상태면 일괄 정리를
// 시작하지 않는지 확인합니다(recorder DB만 정리된 것처럼 보이고 매칭 엔진의
// 실제 메모리는 안 줄어드는 상황 방지).
func TestStartCleanupAllUnresolvedOrdersHandlerBlocksWhenMatchingLagging(t *testing.T) {
	resetCleanupAllStatus(t)
	pub := &fakePublisher{}
	w := httptest.NewRecorder()
	startCleanupAllUnresolvedOrdersHandler(&http.Client{}, "http://unused", pub, &fakeChecker{active: true})(w, httptest.NewRequest(http.MethodPost, "/v1/admin/cleanup-unresolved-orders", nil))

	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d, body=%s", w.Code, http.StatusConflict, w.Body.String())
	}
	var got errorResponse
	json.Unmarshal(w.Body.Bytes(), &got)
	if got.ErrorCode != "MATCHING_ENGINE_LAGGING" {
		t.Errorf("errorCode = %q, want MATCHING_ENGINE_LAGGING", got.ErrorCode)
	}
	cleanupAllMu.Lock()
	status := cleanupAllLatest.Status
	cleanupAllMu.Unlock()
	if status == "IN_PROGRESS" {
		t.Error("차단됐는데도 작업이 시작된 것으로 보임")
	}
}

// TestStartCleanupAllUnresolvedOrdersHandlerForceBypassesLagCheck는 ?force=true면
// 매칭 엔진이 랙 상태여도 시작하는지 확인합니다.
func TestStartCleanupAllUnresolvedOrdersHandlerForceBypassesLagCheck(t *testing.T) {
	resetCleanupAllStatus(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(unresolvedOrdersResponse{})
	}))
	defer srv.Close()

	pub := &fakePublisher{}
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/admin/cleanup-unresolved-orders?force=true", nil)
	startCleanupAllUnresolvedOrdersHandler(srv.Client(), srv.URL, pub, &fakeChecker{active: true})(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d, body=%s", w.Code, http.StatusAccepted, w.Body.String())
	}
	waitForCleanupAllSettled(t)
}

// TestStartCleanupAllUnresolvedOrdersHandlerLagCheckErrorFailsOpen는 랙 확인
// 자체가 실패해도(Redis 순간 장애 등) 정리 작업을 막지 않는지 확인합니다 —
// acceptOrderHandler의 기존 백프레셔 확인과 같은 fail-open 철학.
func TestStartCleanupAllUnresolvedOrdersHandlerLagCheckErrorFailsOpen(t *testing.T) {
	resetCleanupAllStatus(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(unresolvedOrdersResponse{})
	}))
	defer srv.Close()

	pub := &fakePublisher{}
	w := httptest.NewRecorder()
	startCleanupAllUnresolvedOrdersHandler(srv.Client(), srv.URL, pub, &fakeChecker{err: context.DeadlineExceeded})(w, httptest.NewRequest(http.MethodPost, "/v1/admin/cleanup-unresolved-orders", nil))

	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d (랙 확인 실패는 fail-open이어야 함)", w.Code, http.StatusAccepted)
	}
	waitForCleanupAllSettled(t)
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
	defer srv.Close()

	pub := &fakePublisher{}
	w := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		startCleanupAllUnresolvedOrdersHandler(srv.Client(), srv.URL, pub, &fakeChecker{})(w, httptest.NewRequest(http.MethodPost, "/v1/admin/cleanup-unresolved-orders", nil))
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

	// 백그라운드 고루틴이 아직 block 채널에서 대기 중입니다 — 풀어주고, 다음
	// 테스트에 영향을 주지 않도록 종결 상태가 될 때까지 기다립니다(위
	// waitForCleanupAllSettled 설명 참고).
	close(block)
	waitForCleanupAllSettled(t)
}

func TestStartCleanupAllUnresolvedOrdersHandlerConflictWhileInProgress(t *testing.T) {
	resetCleanupAllStatus(t)
	cleanupAllMu.Lock()
	cleanupAllLatest = cleanupAllStatus{Status: "IN_PROGRESS", StartedAt: time.Now().UTC().Format(time.RFC3339)}
	cleanupAllMu.Unlock()

	pub := &fakePublisher{}
	w := httptest.NewRecorder()
	startCleanupAllUnresolvedOrdersHandler(&http.Client{}, "http://unused", pub, &fakeChecker{})(w, httptest.NewRequest(http.MethodPost, "/v1/admin/cleanup-unresolved-orders", nil))

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
