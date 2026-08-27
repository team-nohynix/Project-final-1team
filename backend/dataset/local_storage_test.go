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
	if _, err := storage.SaveBatch(batch, start, end, true); err != nil {
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

func TestLocalStorageExists(t *testing.T) {
	storage := NewLocalStorage(t.TempDir())
	start := time.Date(2026, 7, 27, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)

	res, err := storage.Exists("KRW-BTC", start, end)
	if err != nil {
		t.Fatalf("Exists 실패: %v", err)
	}
	if res.BatchExists || res.StreamExists || res.AllExist() {
		t.Errorf("아무것도 저장 안 했는데 존재한다고 나옴: %+v", res)
	}

	batch := BatchFile{Market: "KRW-BTC", Range: toRange(start, end)}
	if _, err := storage.SaveBatch(batch, start, end, true); err != nil {
		t.Fatalf("SaveBatch 실패: %v", err)
	}

	res, err = storage.Exists("KRW-BTC", start, end)
	if err != nil {
		t.Fatalf("Exists 실패: %v", err)
	}
	if !res.BatchExists || res.StreamExists || res.AllExist() {
		t.Errorf("batch만 저장했는데 결과가 다름: %+v", res)
	}

	stream := StreamFile{Market: "KRW-BTC", Range: toRange(start, end)}
	if _, err := storage.SaveStream(stream, start, end, true); err != nil {
		t.Fatalf("SaveStream 실패: %v", err)
	}

	res, err = storage.Exists("KRW-BTC", start, end)
	if err != nil {
		t.Fatalf("Exists 실패: %v", err)
	}
	if !res.AllExist() {
		t.Errorf("batch/stream 둘 다 저장했는데 AllExist()=false: %+v", res)
	}
}
