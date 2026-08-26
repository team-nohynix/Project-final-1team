package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"orderapi/orderrecords"
	"orderapi/validate"
)

// fakeOrderRecordsStorage는 실제 파일/S3 없이 replayPreviewHandler를 검증하기
// 위한 orderrecords.Storage 구현체입니다 — orderapi/server_test.go의
// fakePublisher와 같은 패턴. byMarket에 없는 마켓은 orderrecords.ErrNotFound를
// 돌려줘, "그 마켓엔 그날 기록이 없다"는 정상 케이스를 재현합니다.
type fakeOrderRecordsStorage struct {
	byMarket map[string][]orderrecords.RecordedOrder
	errs     map[string]error
	dates    []string
	datesErr error
}

func (f *fakeOrderRecordsStorage) ListDates() ([]string, error) {
	return f.dates, f.datesErr
}

func (f *fakeOrderRecordsStorage) Load(market string, start, end time.Time) ([]orderrecords.RecordedOrder, error) {
	if err, ok := f.errs[market]; ok {
		return nil, err
	}
	orders, ok := f.byMarket[market]
	if !ok {
		return nil, orderrecords.ErrNotFound
	}
	return orders, nil
}

func TestReplayPreviewHandlerMissingDate(t *testing.T) {
	storage := &fakeOrderRecordsStorage{}
	w := httptest.NewRecorder()
	replayPreviewHandler(storage)(w, httptest.NewRequest(http.MethodGet, "/v1/jobs/replay-preview", nil))

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestReplayPreviewHandlerInvalidDate(t *testing.T) {
	storage := &fakeOrderRecordsStorage{}
	w := httptest.NewRecorder()
	replayPreviewHandler(storage)(w, httptest.NewRequest(http.MethodGet, "/v1/jobs/replay-preview?date=2026/08/19", nil))

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestReplayPreviewHandlerAggregatesAcrossMarkets(t *testing.T) {
	storage := &fakeOrderRecordsStorage{byMarket: map[string][]orderrecords.RecordedOrder{
		validate.TargetMarkets[0]: {{TS: 1_000}, {TS: 6_000}, {TS: 11_000}}, // 3건, span 10초
		validate.TargetMarkets[1]: {{TS: 2_000}, {TS: 2_500}},               // 2건, span 0.5초
		// 나머지 마켓은 byMarket에 없어 ErrNotFound — 정상적으로 건너뛰어야 함
	}}
	w := httptest.NewRecorder()
	replayPreviewHandler(storage)(w, httptest.NewRequest(http.MethodGet, "/v1/jobs/replay-preview?date=2026-08-19", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", w.Code, http.StatusOK, w.Body.String())
	}
	var got replayPreviewResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("응답 파싱 실패: %v", err)
	}
	if got.Date != "2026-08-19" {
		t.Errorf("Date = %q", got.Date)
	}
	if got.TotalOrders != 5 {
		t.Errorf("TotalOrders = %d, want 5", got.TotalOrders)
	}
	if got.MarketsWithRecords != 2 {
		t.Errorf("MarketsWithRecords = %d, want 2", got.MarketsWithRecords)
	}
	if got.MarketsTotal != len(validate.TargetMarkets) {
		t.Errorf("MarketsTotal = %d, want %d", got.MarketsTotal, len(validate.TargetMarkets))
	}
	// 가장 긴 span(10초)이 반영돼야 함 — 동시 재생이라 합이 아니라 최댓값.
	if got.MaxEventSpanSeconds != 10 {
		t.Errorf("MaxEventSpanSeconds = %v, want 10", got.MaxEventSpanSeconds)
	}
}

func TestReplayPreviewHandlerNoRecordsAnywhere(t *testing.T) {
	storage := &fakeOrderRecordsStorage{byMarket: map[string][]orderrecords.RecordedOrder{}}
	w := httptest.NewRecorder()
	replayPreviewHandler(storage)(w, httptest.NewRequest(http.MethodGet, "/v1/jobs/replay-preview?date=2026-08-19", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
	var got replayPreviewResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("응답 파싱 실패: %v", err)
	}
	if got.TotalOrders != 0 || got.MarketsWithRecords != 0 || got.MaxEventSpanSeconds != 0 {
		t.Errorf("got = %+v", got)
	}
}

func TestReplayPreviewHandlerSkipsErroringMarket(t *testing.T) {
	storage := &fakeOrderRecordsStorage{
		byMarket: map[string][]orderrecords.RecordedOrder{
			validate.TargetMarkets[0]: {{TS: 1_000}, {TS: 2_000}},
		},
		errs: map[string]error{
			validate.TargetMarkets[1]: errTestDownload,
		},
	}
	w := httptest.NewRecorder()
	replayPreviewHandler(storage)(w, httptest.NewRequest(http.MethodGet, "/v1/jobs/replay-preview?date=2026-08-19", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d — 한 마켓 조회 실패가 전체 요청을 막으면 안 됨", w.Code, http.StatusOK)
	}
	var got replayPreviewResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("응답 파싱 실패: %v", err)
	}
	if got.TotalOrders != 2 {
		t.Errorf("TotalOrders = %d, want 2", got.TotalOrders)
	}
}

func TestReplayDatesHandlerReturnsDates(t *testing.T) {
	storage := &fakeOrderRecordsStorage{dates: []string{"2026-08-25", "2026-08-19"}}
	w := httptest.NewRecorder()
	replayDatesHandler(storage)(w, httptest.NewRequest(http.MethodGet, "/v1/jobs/replay-dates", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", w.Code, http.StatusOK, w.Body.String())
	}
	var got replayDatesResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("응답 파싱 실패: %v", err)
	}
	if len(got.Dates) != 2 || got.Dates[0] != "2026-08-25" || got.Dates[1] != "2026-08-19" {
		t.Errorf("Dates = %v", got.Dates)
	}
}

func TestReplayDatesHandlerEmpty(t *testing.T) {
	storage := &fakeOrderRecordsStorage{dates: nil}
	w := httptest.NewRecorder()
	replayDatesHandler(storage)(w, httptest.NewRequest(http.MethodGet, "/v1/jobs/replay-dates", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
	var got replayDatesResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("응답 파싱 실패: %v", err)
	}
	if len(got.Dates) != 0 {
		t.Errorf("Dates = %v, want empty", got.Dates)
	}
}

func TestReplayDatesHandlerStorageError(t *testing.T) {
	storage := &fakeOrderRecordsStorage{datesErr: errTestDownload}
	w := httptest.NewRecorder()
	replayDatesHandler(storage)(w, httptest.NewRequest(http.MethodGet, "/v1/jobs/replay-dates", nil))

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusInternalServerError)
	}
}

var errTestDownload = &testStorageError{"다운로드 실패"}

type testStorageError struct{ msg string }

func (e *testStorageError) Error() string { return e.msg }
