package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"backend/dataset"
	"backend/upbit"
)

func newCollectStatusRequest(jobID string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/v1/collect/"+jobID, nil)
	req.SetPathValue("jobId", jobID)
	return req
}

// TestCollectHandlerAsyncFlow는 202 즉시 응답 → 백그라운드 완료 → 상태 조회로
// 완료 확인까지 전체 흐름을 검증합니다. collect 함수는 채널로 통제해서
// "아직 진행 중"인 순간을 확실하게 만든 뒤에만 완료시킵니다 — 고루틴 스케줄링에
// 우연히 의존하지 않기 위함입니다.
func TestCollectHandlerAsyncFlow(t *testing.T) {
	jobs := newCollectJobStore()
	proceed := make(chan struct{})
	fakeResults := []CollectResult{{Market: "KRW-BTC", Status: "ok", BatchPath: "fake://batch"}}

	fakeCollect := func(storage dataset.Storage, start, end time.Time, onProgress func()) []CollectResult {
		<-proceed
		onProgress()
		return fakeResults
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/collect", strings.NewReader(`{"date":"2026-07-27"}`))
	rec := httptest.NewRecorder()
	collectHandler(newFakeStorage(), jobs, fakeCollect)(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusAccepted, rec.Body.String())
	}
	var accepted collectJob
	if err := json.NewDecoder(rec.Body).Decode(&accepted); err != nil {
		t.Fatalf("응답 파싱 실패: %v", err)
	}
	if accepted.JobID == "" {
		t.Fatal("jobId가 비어있음")
	}
	if accepted.Status != collectStatusInProgress {
		t.Fatalf("status = %q, want %q", accepted.Status, collectStatusInProgress)
	}
	if accepted.Date != "2026-07-27" {
		t.Errorf("date = %q, want 2026-07-27", accepted.Date)
	}
	if accepted.Total != len(upbit.TargetMarkets) {
		t.Errorf("total = %d, want %d", accepted.Total, len(upbit.TargetMarkets))
	}

	// collect가 아직 채널에서 막혀 있으므로, 이 시점에 조회하면 반드시 IN_PROGRESS고
	// 아직 onProgress도 안 불렸으므로 completed는 0이다.
	statusRec := httptest.NewRecorder()
	collectStatusHandler(jobs)(statusRec, newCollectStatusRequest(accepted.JobID))
	var polled collectJob
	if err := json.NewDecoder(statusRec.Body).Decode(&polled); err != nil {
		t.Fatalf("응답 파싱 실패: %v", err)
	}
	if polled.Status != collectStatusInProgress {
		t.Fatalf("완료 전 폴링 status = %q, want %q", polled.Status, collectStatusInProgress)
	}
	if polled.Completed != 0 {
		t.Fatalf("완료 전인데 completed = %d, want 0", polled.Completed)
	}
	if len(polled.Results) != 0 {
		t.Fatalf("완료 전인데 results가 채워짐: %+v", polled.Results)
	}

	close(proceed)

	deadline := time.Now().Add(2 * time.Second)
	var final collectJob
	for time.Now().Before(deadline) {
		rec2 := httptest.NewRecorder()
		collectStatusHandler(jobs)(rec2, newCollectStatusRequest(accepted.JobID))
		if err := json.NewDecoder(rec2.Body).Decode(&final); err != nil {
			t.Fatalf("응답 파싱 실패: %v", err)
		}
		if final.Status == collectStatusCompleted {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if final.Status != collectStatusCompleted {
		t.Fatalf("2초 내에 COMPLETED가 안 됨: %+v", final)
	}
	if len(final.Results) != 1 || final.Results[0].Market != "KRW-BTC" {
		t.Fatalf("unexpected results: %+v", final.Results)
	}
	if final.Completed != 1 {
		t.Fatalf("completed = %d, want 1", final.Completed)
	}
}

func TestCollectHandlerRejectsInvalidDate(t *testing.T) {
	jobs := newCollectJobStore()
	fakeCollect := func(storage dataset.Storage, start, end time.Time, onProgress func()) []CollectResult { return nil }

	req := httptest.NewRequest(http.MethodPost, "/v1/collect", strings.NewReader(`{"date":"invalid"}`))
	rec := httptest.NewRecorder()
	collectHandler(newFakeStorage(), jobs, fakeCollect)(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestCollectStatusHandlerUnknownJob(t *testing.T) {
	jobs := newCollectJobStore()
	rec := httptest.NewRecorder()
	collectStatusHandler(jobs)(rec, newCollectStatusRequest("job_nonexistent"))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}
