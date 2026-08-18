package orderstore

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"trader/order"
)

func TestLocalFileStorageSaveWritesExpectedShape(t *testing.T) {
	storage := NewLocalFileStorage(t.TempDir())
	start := time.Date(2026, 7, 27, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)

	orders := []order.RecordedOrder{
		{TS: 1000, Side: "BUY", Price: "90000000", Quantity: "0.01"},
		{TS: 2000, Side: "SELL", Price: "90100000", Quantity: "0.02"},
	}

	path, err := storage.Save("KRW-BTC", start, end, orders)
	if err != nil {
		t.Fatalf("Save 실패: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("저장된 파일을 못 읽음: %v", err)
	}

	var got orderRecordFile
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("저장된 파일이 JSON이 아님: %v", err)
	}

	if got.Market != "KRW-BTC" {
		t.Errorf("Market = %q, want KRW-BTC", got.Market)
	}
	if got.Range.Start != "2026-07-27T00:00:00Z" || got.Range.End != "2026-07-28T00:00:00Z" {
		t.Errorf("Range = %+v", got.Range)
	}
	if len(got.Orders) != 2 || got.Orders[0].TS != 1000 || got.Orders[1].Side != "SELL" {
		t.Errorf("Orders = %+v", got.Orders)
	}
}

func TestLocalFileStorageKeyLayoutMatchesMarketDataConvention(t *testing.T) {
	start := time.Date(2026, 7, 27, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)

	want := "KRW-BTC/20260727T000000Z_20260728T000000Z_orders.json"
	if got := objectKey("KRW-BTC", start, end); got != want {
		t.Errorf("objectKey = %q, want %q", got, want)
	}
}
