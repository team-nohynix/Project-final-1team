package main

import (
	"context"
	"encoding/json"
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
	claimInfo    session.Info
	claimErr     error
	heartbeatErr error
	releaseErr   error

	lastRunID       string
	lastHeartbeatID string
	lastReleaseID   string
}

func (f *fakeSessionStore) Claim(ctx context.Context, owner, runID string) (session.Info, error) {
	f.lastRunID = runID
	if f.claimErr != nil {
		return session.Info{}, f.claimErr
	}
	return f.claimInfo, nil
}

func (f *fakeSessionStore) Heartbeat(ctx context.Context, sessionID string) error {
	f.lastHeartbeatID = sessionID
	return f.heartbeatErr
}

func (f *fakeSessionStore) Release(ctx context.Context, sessionID string) error {
	f.lastReleaseID = sessionID
	return f.releaseErr
}

func newSessionMux(store session.Store) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/sessions", claimSessionHandler(store))
	mux.HandleFunc("PUT /v1/sessions/{sessionId}/heartbeat", heartbeatSessionHandler(store))
	mux.HandleFunc("DELETE /v1/sessions/{sessionId}", releaseSessionHandler(store))
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

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	if store.lastHeartbeatID != "sess_1" {
		t.Errorf("Heartbeat에 전달된 sessionID = %q, want sess_1", store.lastHeartbeatID)
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
