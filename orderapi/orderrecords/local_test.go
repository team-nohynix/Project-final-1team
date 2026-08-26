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

func TestParseDateFromObjectKey(t *testing.T) {
	cases := []struct {
		name string
		key  string
		want string
		ok   bool
	}{
		{"마켓 경로 포함", "KRW-BTC/20260824T150000Z_20260825T150000Z_orders.json", "2026-08-25", true},
		{"파일명만", "20260824T150000Z_20260825T150000Z_orders.json", "2026-08-25", true},
		{"언더스코어 없음", "garbage.json", "", false},
		{"타임스탬프 파싱 실패", "not-a-timestamp_20260825T150000Z_orders.json", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := parseDateFromObjectKey(c.key)
			if ok != c.ok || (ok && got != c.want) {
				t.Errorf("parseDateFromObjectKey(%q) = (%q, %v), want (%q, %v)", c.key, got, ok, c.want, c.ok)
			}
		})
	}
}

func TestLocalFileStorageListDatesAcrossMarketsDedupedAndSortedDesc(t *testing.T) {
	dir := t.TempDir()
	// -date를 KST 캘린더 하루로 해석하는 trader/main.go와 짝을 맞춰, kst 기준
	// 자정으로 start를 구성합니다(2026-08-26, "전부 KST로 통일" 변경 참고).
	day1 := time.Date(2026, 8, 19, 0, 0, 0, 0, kst)
	day2 := time.Date(2026, 8, 25, 0, 0, 0, 0, kst)

	writeStub := func(market string, start time.Time) {
		path := filepath.Join(dir, filepath.FromSlash(objectKey(market, start, start.Add(24*time.Hour))))
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatalf("디렉터리 생성 실패: %v", err)
		}
		if err := os.WriteFile(path, []byte(`{"orders":[]}`), 0644); err != nil {
			t.Fatalf("파일 쓰기 실패: %v", err)
		}
	}
	writeStub("KRW-BTC", day1)
	writeStub("KRW-ETH", day1) // 같은 날짜, 다른 마켓 — 중복 제거 확인용
	writeStub("KRW-BTC", day2)

	storage := NewLocalFileStorage(dir)
	dates, err := storage.ListDates()
	if err != nil {
		t.Fatalf("ListDates 실패: %v", err)
	}
	if len(dates) != 2 || dates[0] != "2026-08-25" || dates[1] != "2026-08-19" {
		t.Errorf("dates = %v, want [2026-08-25 2026-08-19] (중복 제거 + 최신순)", dates)
	}
}

func TestLocalFileStorageListDatesNoDirectoryYet(t *testing.T) {
	storage := NewLocalFileStorage(filepath.Join(t.TempDir(), "never-created"))
	dates, err := storage.ListDates()
	if err != nil {
		t.Fatalf("ListDates 실패(디렉터리가 없어도 에러가 아니어야 함): %v", err)
	}
	if len(dates) != 0 {
		t.Errorf("dates = %v, want empty", dates)
	}
}
