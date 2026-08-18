package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"backend/dataset"
	"backend/upbit"
)

// fakeStorage는 실제 디스크/S3 없이 fileHandler를 검증하기 위한 인메모리 dataset.Storage
// 구현체입니다. "온디맨드 수집을 트리거하지 않고 이미 있는 파일을 서빙하는" 경로만
// 이걸로 검증합니다 — 수집 트리거 경로(ensureMarketCollected)는 실제 업비트 API 호출이
// 필요해 여기서는 다루지 않고, 실제 서버로 수동 검증한 것으로 대신합니다.
type fakeStorage struct {
	batches map[string]dataset.BatchFile
	streams map[string]dataset.StreamFile
}

func newFakeStorage() *fakeStorage {
	return &fakeStorage{batches: map[string]dataset.BatchFile{}, streams: map[string]dataset.StreamFile{}}
}

func fakeKey(market string, start, end time.Time) string {
	return market + "|" + start.String() + "|" + end.String()
}

func (f *fakeStorage) SaveBatch(b dataset.BatchFile, start, end time.Time) (string, error) {
	f.batches[fakeKey(b.Market, start, end)] = b
	return "fake://" + b.Market + "/batch", nil
}

func (f *fakeStorage) SaveStream(s dataset.StreamFile, start, end time.Time) (string, error) {
	f.streams[fakeKey(s.Market, start, end)] = s
	return "fake://" + s.Market + "/stream", nil
}

func (f *fakeStorage) LoadBatch(market string, start, end time.Time) (dataset.BatchFile, error) {
	b, ok := f.batches[fakeKey(market, start, end)]
	if !ok {
		return dataset.BatchFile{}, dataset.ErrNotFound
	}
	return b, nil
}

func (f *fakeStorage) LoadStream(market string, start, end time.Time) (dataset.StreamFile, error) {
	s, ok := f.streams[fakeKey(market, start, end)]
	if !ok {
		return dataset.StreamFile{}, dataset.ErrNotFound
	}
	return s, nil
}

func TestManifestHandlerReturnsAllTargetMarkets(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/v1/markets/data?date=2026-07-27", nil)
	rec := httptest.NewRecorder()

	manifestHandler()(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	var resp manifestResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("응답 파싱 실패: %v", err)
	}
	if len(resp.Markets) != 20 {
		t.Errorf("Markets 개수 = %d, want 20", len(resp.Markets))
	}
	want := "/v1/markets/KRW-BTC/batch?date=2026-07-27"
	found := false
	for _, m := range resp.Markets {
		if m.Market == "KRW-BTC" {
			found = true
			if m.BatchURL != want {
				t.Errorf("KRW-BTC BatchURL = %q, want %q", m.BatchURL, want)
			}
		}
	}
	if !found {
		t.Error("매니페스트에 KRW-BTC가 없음")
	}
}

func TestManifestHandlerRejectsInvalidDate(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/v1/markets/data?date=invalid", nil)
	rec := httptest.NewRecorder()

	manifestHandler()(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

// newFileHandlerMux는 fileHandler를 실제 라우트 패턴({market}/{kind})으로 등록한 mux를
// 반환합니다 — r.PathValue()는 ServeMux를 통해 매칭돼야 채워지므로 직접 호출로는 검증 불가.
func newFileHandlerMux(storage dataset.Storage) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/markets/{market}/{kind}", fileHandler(storage))
	return mux
}

func TestFileHandlerServesExistingBatchWithoutCollecting(t *testing.T) {
	storage := newFakeStorage()
	// parseDate(server.go)가 이제 KST 자정으로 파싱하므로, 여기서도 같은 타임존으로
	// 구성해야 fileHandler가 계산하는 start/end와 일치해서 캐시 히트가 됩니다.
	start := time.Date(2026, 7, 27, 0, 0, 0, 0, upbit.KST)
	end := start.Add(24 * time.Hour)
	storage.batches[fakeKey("KRW-BTC", start, end)] = dataset.BatchFile{Market: "KRW-BTC"}

	req := httptest.NewRequest(http.MethodGet, "/v1/markets/KRW-BTC/batch?date=2026-07-27", nil)
	rec := httptest.NewRecorder()
	newFileHandlerMux(storage).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", rec.Code, rec.Body.String())
	}

	var got dataset.BatchFile
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("응답 파싱 실패: %v", err)
	}
	if got.Market != "KRW-BTC" {
		t.Errorf("Market = %q, want KRW-BTC", got.Market)
	}
}

func TestFileHandlerRejectsInvalidKind(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/v1/markets/KRW-BTC/candles?date=2026-07-27", nil)
	rec := httptest.NewRecorder()
	newFileHandlerMux(newFakeStorage()).ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestFileHandlerRejectsUnsupportedMarket(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/v1/markets/KRW-NOTREAL/batch?date=2026-07-27", nil)
	rec := httptest.NewRecorder()
	newFileHandlerMux(newFakeStorage()).ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestFileHandlerRejectsInvalidDate(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/v1/markets/KRW-BTC/batch?date=invalid", nil)
	rec := httptest.NewRecorder()
	newFileHandlerMux(newFakeStorage()).ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}
