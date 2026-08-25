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

	path, err := storage.Save("KRW-BTC", start, end, orders, true)
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

func TestLocalFileStorageSaveMergesAcrossCalls(t *testing.T) {
	storage := NewLocalFileStorage(t.TempDir())
	start := time.Date(2026, 7, 27, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)

	first := []order.RecordedOrder{{TS: 1000, Side: "BUY", Price: "90000000", Quantity: "0.01"}}
	if _, err := storage.Save("KRW-BTC", start, end, first, true); err != nil {
		t.Fatalf("첫 번째 Save 실패: %v", err)
	}

	second := []order.RecordedOrder{{TS: 2000, Side: "SELL", Price: "90100000", Quantity: "0.02"}}
	path, err := storage.Save("KRW-BTC", start, end, second, false)
	if err != nil {
		t.Fatalf("두 번째 Save 실패: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("저장된 파일을 못 읽음: %v", err)
	}
	var got orderRecordFile
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("저장된 파일이 JSON이 아님: %v", err)
	}

	if len(got.Orders) != 2 || got.Orders[0].TS != 1000 || got.Orders[1].TS != 2000 {
		t.Errorf("두 번째 Save 이후 Orders = %+v, want 1000/2000 두 건이 누적되어야 함", got.Orders)
	}
}

func TestLocalFileStorageSaveResetClearsPriorRunData(t *testing.T) {
	// 실전 사고 재현: 같은 날짜(-date)로 트레이더를 재실행하면, 이전 실행이 남긴
	// 기록이 이번 실행의 기록과 섞여선 안 된다 — reset=true는 그걸 막아야 한다.
	storage := NewLocalFileStorage(t.TempDir())
	start := time.Date(2026, 7, 27, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)

	staleFromPreviousRun := []order.RecordedOrder{{TS: 500, Side: "BUY", Price: "1", Quantity: "1"}}
	if _, err := storage.Save("KRW-BTC", start, end, staleFromPreviousRun, false); err != nil {
		t.Fatalf("이전 실행 데이터 저장 실패: %v", err)
	}

	freshFromNewRun := []order.RecordedOrder{{TS: 9000, Side: "SELL", Price: "2", Quantity: "2"}}
	path, err := storage.Save("KRW-BTC", start, end, freshFromNewRun, true)
	if err != nil {
		t.Fatalf("reset Save 실패: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("저장된 파일을 못 읽음: %v", err)
	}
	var got orderRecordFile
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("저장된 파일이 JSON이 아님: %v", err)
	}

	if len(got.Orders) != 1 || got.Orders[0].TS != 9000 {
		t.Errorf("reset=true인데 이전 실행 데이터가 남아있음: %+v", got.Orders)
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
