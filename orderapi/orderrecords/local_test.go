package orderrecords

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLocalFileStorageLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	market := "KRW-BTC"
	start := time.Date(2026, 8, 19, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)

	// trader/orderstore가 실제로 쓰는 것과 같은 모양의 파일을 미리 만들어둡니다
	// (다른 필드도 실제 파일엔 있지만, 이 패키지는 orders[].ts만 읽으므로 그
	// 필드들도 그대로 두되 디코딩 검증은 ts만 합니다).
	path := filepath.Join(dir, filepath.FromSlash(objectKey(market, start, end)))
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("디렉터리 생성 실패: %v", err)
	}
	raw := `{"market":"KRW-BTC","range":{"start":"2026-08-19T00:00:00Z","end":"2026-08-20T00:00:00Z"},"orders":[` +
		`{"ts":1000,"side":"BUY","price":"90000000","quantity":"0.01"},` +
		`{"ts":5000,"side":"SELL","price":"90100000","quantity":"0.02"}` +
		`]}`
	if err := os.WriteFile(path, []byte(raw), 0644); err != nil {
		t.Fatalf("파일 쓰기 실패: %v", err)
	}

	storage := NewLocalFileStorage(dir)
	orders, err := storage.Load(market, start, end)
	if err != nil {
		t.Fatalf("Load 실패: %v", err)
	}
	if len(orders) != 2 || orders[0].TS != 1000 || orders[1].TS != 5000 {
		t.Errorf("orders = %+v", orders)
	}
}

func TestLocalFileStorageLoadNotFound(t *testing.T) {
	dir := t.TempDir()
	storage := NewLocalFileStorage(dir)

	start := time.Date(2026, 8, 19, 0, 0, 0, 0, time.UTC)
	_, err := storage.Load("KRW-BTC", start, start.Add(24*time.Hour))
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestDecodeOrderRecordFile(t *testing.T) {
	body := []byte(`{"market":"KRW-BTC","orders":[{"ts":10},{"ts":20},{"ts":30}]}`)
	orders, err := decodeOrderRecordFile(body)
	if err != nil {
		t.Fatalf("디코딩 실패: %v", err)
	}
	if len(orders) != 3 || orders[2].TS != 30 {
		t.Errorf("orders = %+v", orders)
	}
}

func TestDecodeOrderRecordFileInvalid(t *testing.T) {
	_, err := decodeOrderRecordFile([]byte(`not json`))
	if err == nil {
		t.Error("에러를 기대했는데 nil")
	}
}
