package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func resetDroppedBuckets() {
	droppedMu.Lock()
	droppedBuckets = make(map[int64]int64)
	droppedMu.Unlock()
}

func TestDroppedOrdersHandlerMissingRange(t *testing.T) {
	resetDroppedBuckets()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/dropped-orders", nil)
	droppedOrdersHandler()(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	var got errorResponse
	json.NewDecoder(rec.Body).Decode(&got)
	if got.ErrorCode != "MISSING_RANGE" {
		t.Errorf("errorCode = %q, want MISSING_RANGE", got.ErrorCode)
	}
}

func TestDroppedOrdersHandlerInvalidRange(t *testing.T) {
	resetDroppedBuckets()
	rec := httptest.NewRecorder()
	now := time.Now().UTC()
	from := now.Format(time.RFC3339)
	to := now.Add(-time.Minute).Format(time.RFC3339) // to보다 이전 — 순서가 뒤집힘
	req := httptest.NewRequest(http.MethodGet, "/v1/dropped-orders?from="+from+"&to="+to, nil)
	droppedOrdersHandler()(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	var got errorResponse
	json.NewDecoder(rec.Body).Decode(&got)
	if got.ErrorCode != "INVALID_RANGE" {
		t.Errorf("errorCode = %q, want INVALID_RANGE", got.ErrorCode)
	}
}

func TestDroppedOrdersHandlerRangeTooWide(t *testing.T) {
	resetDroppedBuckets()
	rec := httptest.NewRecorder()
	now := time.Now().UTC()
	from := now.Add(-25 * time.Hour).Format(time.RFC3339)
	to := now.Format(time.RFC3339)
	req := httptest.NewRequest(http.MethodGet, "/v1/dropped-orders?from="+from+"&to="+to, nil)
	droppedOrdersHandler()(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	var got errorResponse
	json.NewDecoder(rec.Body).Decode(&got)
	if got.ErrorCode != "RANGE_TOO_WIDE" {
		t.Errorf("errorCode = %q, want RANGE_TOO_WIDE", got.ErrorCode)
	}
}

func TestDroppedOrdersHandlerReturnsZeroFilledBucketsAndCounts(t *testing.T) {
	resetDroppedBuckets()
	recordDroppedOrder()
	recordDroppedOrder()

	now := time.Now().UTC()
	from := now.Add(-4 * time.Minute).Format(time.RFC3339)
	to := now.Format(time.RFC3339)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/dropped-orders?from="+from+"&to="+to, nil)
	droppedOrdersHandler()(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", rec.Code, rec.Body.String())
	}
	var got struct {
		Series []droppedBucketDTO `json:"series"`
	}
	json.NewDecoder(rec.Body).Decode(&got)

	if len(got.Series) < 4 {
		t.Fatalf("series 길이 = %d, want >= 4 (분 단위 버킷)", len(got.Series))
	}

	var total int64
	for _, b := range got.Series {
		total += b.Dropped
	}
	if total != 2 {
		t.Errorf("전체 dropped 합계 = %d, want 2", total)
	}

	last := got.Series[len(got.Series)-1]
	if last.Dropped != 2 {
		t.Errorf("마지막(현재) 버킷 dropped = %d, want 2 (방금 recordDroppedOrder 2회)", last.Dropped)
	}
}

func TestRecordDroppedOrderPrunesOldBuckets(t *testing.T) {
	resetDroppedBuckets()

	oldKey := time.Now().UTC().Add(-2 * droppedRetention).Truncate(droppedBucket).Unix()
	droppedMu.Lock()
	droppedBuckets[oldKey] = 5
	droppedMu.Unlock()

	recordDroppedOrder()

	droppedMu.Lock()
	_, stillThere := droppedBuckets[oldKey]
	droppedMu.Unlock()
	if stillThere {
		t.Errorf("보존 기간(%s)보다 오래된 버킷이 정리되지 않음", droppedRetention)
	}
}
