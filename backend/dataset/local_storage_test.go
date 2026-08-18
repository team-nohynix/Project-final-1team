package dataset

import (
	"errors"
	"testing"
	"time"
)

func TestLocalStorageSaveLoadRoundTrip(t *testing.T) {
	storage := NewLocalStorage(t.TempDir())
	start := time.Date(2026, 7, 27, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)

	batch := BatchFile{Market: "KRW-BTC", Range: toRange(start, end), Candles: BatchCandles{
		Days: []CandleOHLCV{{TS: 1, Open: 1, High: 1, Low: 1, Close: 1, Volume: 1}},
	}}
	if _, err := storage.SaveBatch(batch, start, end); err != nil {
		t.Fatalf("SaveBatch 실패: %v", err)
	}

	got, err := storage.LoadBatch("KRW-BTC", start, end)
	if err != nil {
		t.Fatalf("LoadBatch 실패: %v", err)
	}
	if got.Market != "KRW-BTC" || len(got.Candles.Days) != 1 {
		t.Errorf("LoadBatch 결과 = %+v, 저장한 내용과 달라짐", got)
	}
}

func TestLocalStorageLoadMissingReturnsErrNotFound(t *testing.T) {
	storage := NewLocalStorage(t.TempDir())
	start := time.Date(2026, 7, 27, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)

	_, err := storage.LoadBatch("KRW-BTC", start, end)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}

	_, err = storage.LoadStream("KRW-BTC", start, end)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}
