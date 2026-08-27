package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"orderapi/session"
)

// fakeSessionStore는 실제 Redis 없이 세션 핸들러를 검증하기 위한
// session.Store 구현체입니다.
type fakeSessionStore struct {
	claimInfo         session.Info
	claimErr          error
	heartbeatErr      error
	heartbeatStopFlag bool
	releaseErr        error
	releaseRecord     session.RunRecord
	releaseFinalized  bool
	lastRunRec        session.RunRecord
	lastRunFound      bool
	lastRunErr        error
	prevRunRec        session.RunRecord
	prevRunFound      bool
	prevRunErr        error
	prevRun2Rec       session.RunRecord
	prevRun2Found     bool
	prevRun2Err       error
	runHistoryRecs    []session.RunRecord
	runHistoryErr     error
	requestStopErr    error
	appendNoteErr     error

	lastRunID       string
	lastSpeed       float64
	lastDate        string
	lastHeartbeatID string
	lastReleaseID   string
	lastOutcome     session.RunOutcome
	lastStopRunID   string
	lastNoteRunID   string
	lastNote        string
}

func (f *fakeSessionStore) Claim(ctx context.Context, owner, runID, date string, speed float64) (session.Info, error) {
	f.lastRunID = runID
	f.lastSpeed = speed
	f.lastDate = date
	if f.claimErr != nil {
		return session.Info{}, f.claimErr
	}
	return f.claimInfo, nil
}

func (f *fakeSessionStore) Heartbeat(ctx context.Context, sessionID string) (bool, error) {
	f.lastHeartbeatID = sessionID
	if f.heartbeatErr != nil {
		return false, f.heartbeatErr
	}
	return f.heartbeatStopFlag, nil
}

func (f *fakeSessionStore) RequestStop(ctx context.Context, runID string) error {
	f.lastStopRunID = runID
	return f.requestStopErr
}

func (f *fakeSessionStore) Release(ctx context.Context, sessionID string, outcome session.RunOutcome) (session.RunRecord, bool, error) {
	f.lastReleaseID = sessionID
	f.lastOutcome = outcome
	if f.releaseErr != nil {
		return session.RunRecord{}, false, f.releaseErr
	}
	return f.releaseRecord, f.releaseFinalized, nil
}

func (f *fakeSessionStore) LastRun(ctx context.Context) (session.RunRecord, bool, error) {
	return f.lastRunRec, f.lastRunFound, f.lastRunErr
}

func (f *fakeSessionStore) PreviousRun(ctx context.Context) (session.RunRecord, bool, error) {
	return f.prevRunRec, f.prevRunFound, f.prevRunErr
}

func (f *fakeSessionStore) PreviousRun2(ctx context.Context) (session.RunRecord, bool, error) {
	return f.prevRun2Rec, f.prevRun2Found, f.prevRun2Err
}

func (f *fakeSessionStore) RunHistory(ctx context.Context) ([]session.RunRecord, error) {
	return f.runHistoryRecs, f.runHistoryErr
}

func (f *fakeSessionStore) AppendLastRunNote(ctx context.Context, runID, note string) error {
	f.lastNoteRunID = runID
	f.lastNote = note
	return f.appendNoteErr
}

// newSessionMux는 미종결 주문 정리(sessioncleanup.go)를 건너뛰는 기본
// 구성으로 만듭니다(recorderURL 빈 문자열) — 그 정리 로직 자체를 검증하는
// 테스트는 releaseSessionHandler를 직접 호출해서 recorderURL을 채웁니다.
func newSessionMux(store session.Store) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/sessions", claimSessionHandler(store))
	mux.HandleFunc("PUT /v1/sessions/{sessionId}/heartbeat", heartbeatSessionHandler(store))
	mux.HandleFunc("POST /v1/sessions/{runId}/stop", stopRunHandler(store))
	mux.HandleFunc("DELETE /v1/sessions/{sessionId}", releaseSessionHandler(store, &fakePublisher{}, &http.Client{}, "", &fakeChecker{}))
	mux.HandleFunc("GET /v1/sessions/last-run", lastRunHandler(store))
	mux.HandleFunc("GET /v1/sessions/previous-run", previousRunHandler(store))
	mux.HandleFunc("GET /v1/sessions/runs", runHistoryHandler(store))
	return mux
}

func TestClaimSessionSuccess(t *testing.T) {
	store := &fakeSessionStore{claimInfo: session.Info{
		SessionID: "sess_1", Owner: "trader", ClaimedAt: time.Now().UTC(), TTL: 30 * time.Second,
	}}
	mux := newSessionMux(store)

	req := httptest.NewRequest(http.MethodPost, "/v1/sessions", strings.NewReader(`{"owner":"trader"}`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201, body=%s", rec.Code, rec.Body.String())
	}
	var got claimResponse
	json.NewDecoder(rec.Body).Decode(&got)
	if got.SessionID != "sess_1" || got.Owner != "trader" || got.TTLSeconds != 30 {
		t.Errorf("got = %+v", got)
	}
}

func TestClaimSessionRunIDPassedThrough(t *testing.T) {
	store := &fakeSessionStore{claimInfo: session.Info{
		SessionID: "run_1.mem_1", RunID: "run_1", Owner: "replayengine", ClaimedAt: time.Now().UTC(), TTL: 30 * time.Second,
	}}
	mux := newSessionMux(store)

	req := httptest.NewRequest(http.MethodPost, "/v1/sessions", strings.NewReader(`{"owner":"replayengine","runId":"run_1"}`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201, body=%s", rec.Code, rec.Body.String())
	}
	if store.lastRunID != "run_1" {
		t.Errorf("Claim에 전달된 runID = %q, want run_1", store.lastRunID)
	}
	var got claimResponse
	json.NewDecoder(rec.Body).Decode(&got)
	if got.RunID != "run_1" || got.SessionID != "run_1.mem_1" {
		t.Errorf("got = %+v", got)
	}
}

func TestClaimSessionMissingOwner(t *testing.T) {
	mux := newSessionMux(&fakeSessionStore{})
	req := httptest.NewRequest(http.MethodPost, "/v1/sessions", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestClaimSessionConflict(t *testing.T) {
	claimedAt := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	store := &fakeSessionStore{claimErr: &session.ConflictError{
		Current: session.Info{SessionID: "sess_other", Owner: "replayengine", ClaimedAt: claimedAt},
	}}
	mux := newSessionMux(store)

	req := httptest.NewRequest(http.MethodPost, "/v1/sessions", strings.NewReader(`{"owner":"trader"}`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409, body=%s", rec.Code, rec.Body.String())
	}
	var got errorResponse
	json.NewDecoder(rec.Body).Decode(&got)
	if got.ErrorCode != "SESSION_ALREADY_ACTIVE" {
		t.Errorf("errorCode = %q, want SESSION_ALREADY_ACTIVE", got.ErrorCode)
	}
}

func TestHeartbeatSessionSuccess(t *testing.T) {
	store := &fakeSessionStore{}
	mux := newSessionMux(store)

	req := httptest.NewRequest(http.MethodPut, "/v1/sessions/sess_1/heartbeat", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", rec.Code, rec.Body.String())
	}
	if store.lastHeartbeatID != "sess_1" {
		t.Errorf("Heartbeat에 전달된 sessionID = %q, want sess_1", store.lastHeartbeatID)
	}
	var got heartbeatResponse
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("응답 파싱 실패: %v", err)
	}
	if got.StopRequested {
		t.Errorf("stopRequested = true, want false (정지 요청 안 한 경우)")
	}
}

func TestHeartbeatSessionStopRequested(t *testing.T) {
	store := &fakeSessionStore{heartbeatStopFlag: true}
	mux := newSessionMux(store)

	req := httptest.NewRequest(http.MethodPut, "/v1/sessions/sess_1/heartbeat", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", rec.Code, rec.Body.String())
	}
	var got heartbeatResponse
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("응답 파싱 실패: %v", err)
	}
	if !got.StopRequested {
		t.Errorf("stopRequested = false, want true (RequestStop이 이미 불린 그룹)")
	}
}

func TestStopRunSuccess(t *testing.T) {
	store := &fakeSessionStore{}
	mux := newSessionMux(store)

	req := httptest.NewRequest(http.MethodPost, "/v1/sessions/run_1/stop", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204, body=%s", rec.Code, rec.Body.String())
	}
	if store.lastStopRunID != "run_1" {
		t.Errorf("RequestStop에 전달된 runID = %q, want run_1", store.lastStopRunID)
	}
}

func TestStopRunNoActiveRun(t *testing.T) {
	store := &fakeSessionStore{requestStopErr: session.ErrNotActive}
	mux := newSessionMux(store)

	req := httptest.NewRequest(http.MethodPost, "/v1/sessions/run_gone/stop", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404, body=%s", rec.Code, rec.Body.String())
	}
	var got errorResponse
	json.NewDecoder(rec.Body).Decode(&got)
	if got.ErrorCode != "NO_ACTIVE_RUN" {
		t.Errorf("errorCode = %q, want NO_ACTIVE_RUN", got.ErrorCode)
	}
}

func TestHeartbeatSessionNotActive(t *testing.T) {
	store := &fakeSessionStore{heartbeatErr: session.ErrNotActive}
	mux := newSessionMux(store)

	req := httptest.NewRequest(http.MethodPut, "/v1/sessions/sess_stale/heartbeat", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	var got errorResponse
	json.NewDecoder(rec.Body).Decode(&got)
	if got.ErrorCode != "SESSION_NOT_ACTIVE" {
		t.Errorf("errorCode = %q, want SESSION_NOT_ACTIVE", got.ErrorCode)
	}
}

func TestReleaseSessionSuccess(t *testing.T) {
	store := &fakeSessionStore{}
	mux := newSessionMux(store)

	req := httptest.NewRequest(http.MethodDelete, "/v1/sessions/sess_1", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	if store.lastReleaseID != "sess_1" {
		t.Errorf("Release에 전달된 sessionID = %q, want sess_1", store.lastReleaseID)
	}
}

func TestReleaseSessionNotActive(t *testing.T) {
	store := &fakeSessionStore{releaseErr: session.ErrNotActive}
	mux := newSessionMux(store)

	req := httptest.NewRequest(http.MethodDelete, "/v1/sessions/sess_gone", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestReleaseSessionNoBodyDefaultsToCompleted(t *testing.T) {
	store := &fakeSessionStore{}
	mux := newSessionMux(store)

	req := httptest.NewRequest(http.MethodDelete, "/v1/sessions/sess_1", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	if store.lastOutcome.Status != session.RunStatusCompleted {
		t.Errorf("outcome.Status = %q, want %q (본문 없을 때 기본값)", store.lastOutcome.Status, session.RunStatusCompleted)
	}
}

func TestReleaseSessionFailedOutcome(t *testing.T) {
	store := &fakeSessionStore{}
	mux := newSessionMux(store)

	req := httptest.NewRequest(http.MethodDelete, "/v1/sessions/sess_1", strings.NewReader(`{"status":"FAILED","message":"보드 연결 실패"}`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	if store.lastOutcome.Status != session.RunStatusFailed || store.lastOutcome.Message != "보드 연결 실패" {
		t.Errorf("outcome = %+v", store.lastOutcome)
	}
}

// TestReleaseSessionStoppedOutcome은 2026-08-26 버그 수정을 검증합니다 —
// FAILED만 확인하고 STOPPED을 안 걸러서, 사용자가 "중지"로 직접 멈춘
// 실행(trader/replayengine이 status="STOPPED"로 보고)도 전부 COMPLETED로
// 저장되던 문제(실행 결과 화면에서 배지는 "정상 종료", 메시지는 "사용자
// 요청으로 정지됨"으로 서로 어긋나 보였음).
func TestReleaseSessionStoppedOutcome(t *testing.T) {
	store := &fakeSessionStore{}
	mux := newSessionMux(store)

	req := httptest.NewRequest(http.MethodDelete, "/v1/sessions/sess_1", strings.NewReader(`{"status":"STOPPED","message":"사용자 요청으로 정지됨"}`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	if store.lastOutcome.Status != session.RunStatusStopped || store.lastOutcome.Message != "사용자 요청으로 정지됨" {
		t.Errorf("outcome = %+v, want Status=%q", store.lastOutcome, session.RunStatusStopped)
	}
}

func TestLastRunHandlerFound(t *testing.T) {
	started := time.Date(2026, 8, 12, 9, 0, 0, 0, time.UTC)
	ended := started.Add(3 * time.Minute)
	store := &fakeSessionStore{lastRunFound: true, lastRunRec: session.RunRecord{
		RunID: "run_1", Owner: "trader", Status: session.RunStatusCompleted,
		StartedAt: started, EndedAt: ended, Message: "일부 마켓 실패: 2개",
	}}
	mux := newSessionMux(store)

	req := httptest.NewRequest(http.MethodGet, "/v1/sessions/last-run", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", rec.Code, rec.Body.String())
	}
	var got lastRunResponse
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("응답 파싱 실패: %v", err)
	}
	if got.Status != "COMPLETED" || got.EndedAt == "" || got.Message == "" {
		t.Errorf("got = %+v", got)
	}
}

// TestLastRunHandlerPreservesSubSecondPrecision — 2026-08-27, "주문 유실"/
// "매수매도 총량 불일치"가 실행이 끝나는 바로 그 초에 들어온 주문들을 잘라내
// 매 실행마다 몇 건씩 허수로 잡히던 사고의 회귀 테스트입니다(실측:
// 00:25:10.000~00:25:10.322 사이 13건). RFC3339(초 단위)로 포맷하면 이
// 밀리초가 통째로 사라져, 프론트가 그대로 recorder 쿼리의 [from, to) 경계로
// 쓰면서 그 구간 주문들이 잘려나갔습니다. EndedAt이 정각이 아닌 밀리초를
// 가지고 있으면 응답 문자열에도 그대로 남아있어야 합니다.
func TestLastRunHandlerPreservesSubSecondPrecision(t *testing.T) {
	started := time.Date(2026, 8, 27, 0, 20, 0, 0, time.UTC)
	ended := time.Date(2026, 8, 27, 0, 25, 10, 322000000, time.UTC) // 00:25:10.322
	store := &fakeSessionStore{lastRunFound: true, lastRunRec: session.RunRecord{
		RunID: "run_1", Owner: "replayengine", Status: session.RunStatusCompleted,
		StartedAt: started, EndedAt: ended,
	}}
	mux := newSessionMux(store)

	req := httptest.NewRequest(http.MethodGet, "/v1/sessions/last-run", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	var got lastRunResponse
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("응답 파싱 실패: %v", err)
	}
	if !strings.Contains(got.EndedAt, ".322") {
		t.Fatalf("EndedAt = %q, want 밀리초(.322)가 남아있어야 함", got.EndedAt)
	}

	// 프론트가 이 문자열을 그대로 recorder의 orders/summary·orders/integrity
	// 쿼리 파라미터로 쓰므로, recorder 쪽 time.Parse(time.RFC3339, ...)가 이
	// 문자열을 밀리초까지 정확히 되살릴 수 있어야 합니다(Go는 레이아웃에 소수점
	// 자리가 없어도 파싱 시엔 관대하게 받아들이지만, 왕복 자체를 직접 검증).
	reparsed, err := time.Parse(time.RFC3339, got.EndedAt)
	if err != nil {
		t.Fatalf("recorder 쪽 파싱 실패: %v", err)
	}
	if !reparsed.Equal(ended) {
		t.Errorf("재파싱된 시각 = %v, want %v (밀리초 유실)", reparsed, ended)
	}
}

func TestLastRunHandlerNeverRun(t *testing.T) {
	store := &fakeSessionStore{lastRunFound: false}
	mux := newSessionMux(store)

	req := httptest.NewRequest(http.MethodGet, "/v1/sessions/last-run", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestRunHistoryHandlerReturnsRecords(t *testing.T) {
	started := time.Date(2026, 8, 25, 21, 40, 13, 0, time.UTC)
	ended := started.Add(5*time.Minute + 41*time.Second)
	store := &fakeSessionStore{runHistoryRecs: []session.RunRecord{
		{RunID: "run_2", Owner: "replayengine", Status: session.RunStatusCompleted, StartedAt: started, EndedAt: ended},
		{RunID: "run_1", Owner: "replayengine", Status: session.RunStatusFailed, StartedAt: started.Add(-time.Hour), EndedAt: started.Add(-time.Hour + time.Minute)},
	}}
	mux := newSessionMux(store)

	req := httptest.NewRequest(http.MethodGet, "/v1/sessions/runs", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", rec.Code, rec.Body.String())
	}
	var got []lastRunResponse
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("응답 파싱 실패: %v", err)
	}
	if len(got) != 2 || got[0].RunID != "run_2" || got[1].RunID != "run_1" {
		t.Fatalf("got = %+v", got)
	}
	if got[0].Status != "COMPLETED" || got[0].EndedAt == "" {
		t.Errorf("got[0] = %+v", got[0])
	}
}

func TestRunHistoryHandlerEmptyIsNotAnError(t *testing.T) {
	store := &fakeSessionStore{runHistoryRecs: nil}
	mux := newSessionMux(store)

	req := httptest.NewRequest(http.MethodGet, "/v1/sessions/runs", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", rec.Code, rec.Body.String())
	}
	var got []lastRunResponse
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("응답 파싱 실패: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got = %+v, want empty", got)
	}
}

func TestRunHistoryHandlerStoreError(t *testing.T) {
	store := &fakeSessionStore{runHistoryErr: errors.New("redis 장애")}
	mux := newSessionMux(store)

	req := httptest.NewRequest(http.MethodGet, "/v1/sessions/runs", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
}

func TestPreviousRunHandlerFound(t *testing.T) {
	started := time.Date(2026, 8, 18, 9, 0, 0, 0, time.UTC)
	ended := started.Add(5 * time.Minute)
	store := &fakeSessionStore{prevRunFound: true, prevRunRec: session.RunRecord{
		RunID: "run_0", Owner: "replayengine", Status: session.RunStatusCompleted,
		StartedAt: started, EndedAt: ended,
	}}
	mux := newSessionMux(store)

	req := httptest.NewRequest(http.MethodGet, "/v1/sessions/previous-run", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", rec.Code, rec.Body.String())
	}
	var got lastRunResponse
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("응답 파싱 실패: %v", err)
	}
	if got.RunID != "run_0" || got.Owner != "replayengine" || got.EndedAt == "" {
		t.Errorf("got = %+v", got)
	}
}

func TestPreviousRunHandlerNone(t *testing.T) {
	store := &fakeSessionStore{prevRunFound: false}
	mux := newSessionMux(store)

	req := httptest.NewRequest(http.MethodGet, "/v1/sessions/previous-run", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}
