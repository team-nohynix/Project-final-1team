package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFetchManifestParsesResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/markets/data" {
			t.Errorf("예상치 못한 경로: %s", r.URL.Path)
		}
		if got := r.URL.Query().Get("date"); got != "2026-07-27" {
			t.Errorf("date 쿼리파라미터 = %q, want 2026-07-27", got)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"date": "2026-07-27",
			"markets": [
				{"market": "KRW-BTC", "batchUrl": "/v1/markets/KRW-BTC/batch?date=2026-07-27", "streamUrl": "/v1/markets/KRW-BTC/stream?date=2026-07-27"}
			]
		}`))
	}))
	defer srv.Close()

	m, err := FetchManifest(context.Background(), srv.Client(), srv.URL, "2026-07-27")
	if err != nil {
		t.Fatalf("FetchManifest 실패: %v", err)
	}
	if len(m.Markets) != 1 || m.Markets[0].Market != "KRW-BTC" {
		t.Errorf("Markets = %+v, 1개(KRW-BTC)를 기대함", m.Markets)
	}
}

func TestFetchBatchAndStream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/batch"):
			w.Write([]byte(`{"market":"KRW-BTC","range":{"start":"2026-07-27T00:00:00Z","end":"2026-07-28T00:00:00Z"},"candles":{"days":[{"ts":1,"open":1,"high":1,"low":1,"close":1,"volume":1}]}}`))
		case strings.HasSuffix(r.URL.Path, "/stream"):
			w.Write([]byte(`{"market":"KRW-BTC","range":{"start":"2026-07-27T00:00:00Z","end":"2026-07-28T00:00:00Z"},"events":[{"type":"trade_tick","ts":1,"price":100,"volume":1,"side":"BUY"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	batch, err := FetchBatch(context.Background(), srv.Client(), srv.URL, "/v1/markets/KRW-BTC/batch")
	if err != nil {
		t.Fatalf("FetchBatch 실패: %v", err)
	}
	if len(batch.Candles.Days) != 1 {
		t.Errorf("batch.Candles.Days 길이 = %d, want 1", len(batch.Candles.Days))
	}

	stream, err := FetchStream(context.Background(), srv.Client(), srv.URL, "/v1/markets/KRW-BTC/stream")
	if err != nil {
		t.Fatalf("FetchStream 실패: %v", err)
	}
	if len(stream.Events) != 1 || stream.Events[0].Type != "trade_tick" {
		t.Errorf("stream.Events = %+v", stream.Events)
	}
}

func TestFetchJSONReturnsErrorOnNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	_, err := FetchManifest(context.Background(), srv.Client(), srv.URL, "2026-07-27")
	if err == nil {
		t.Fatal("500 응답에 대해 에러를 기대했으나 nil")
	}
}

func TestFetchJSONReturnsErrorOnMalformedBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("이건 JSON이 아님"))
	}))
	defer srv.Close()

	_, err := FetchManifest(context.Background(), srv.Client(), srv.URL, "2026-07-27")
	if err == nil {
		t.Fatal("깨진 JSON 응답에 대해 에러를 기대했으나 nil")
	}
}
