package orderstore

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLocalFileStorageLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	market := "KRW-BTC"
	start := time.Date(2026, 7, 27, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)

	// trader/orderstore가 실제로 쓰는 것과 같은 모양의 파일을 미리 만들어둡니다.
	path := filepath.Join(dir, filepath.FromSlash(objectKey(market, start, end)))
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("디렉터리 생성 실패: %v", err)
	}
	body, _ := json.Marshal(orderRecordFile{
		Market: market,
		Orders: []RecordedOrder{
			{TS: 1000, Side: "BUY", Price: "90000000", Quantity: "0.01"},
			{TS: 2000, Side: "SELL", Price: "90100000", Quantity: "0.02"},
		},
	})
	if err := os.WriteFile(path, body, 0644); err != nil {
		t.Fatalf("파일 쓰기 실패: %v", err)
	}

	storage := NewLocalFileStorage(dir)
	orders, err := storage.Load(market, start, end)
	if err != nil {
		t.Fatalf("Load 실패: %v", err)
	}
	if len(orders) != 2 || orders[0].TS != 1000 || orders[1].Side != "SELL" {
		t.Errorf("orders = %+v", orders)
	}
}

func TestLocalFileStorageLoadNotFound(t *testing.T) {
	dir := t.TempDir()
	storage := NewLocalFileStorage(dir)

	start := time.Date(2026, 7, 27, 0, 0, 0, 0, time.UTC)
	_, err := storage.Load("KRW-BTC", start, start.Add(24*time.Hour))
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}
