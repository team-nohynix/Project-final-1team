package orderrecords

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// LocalFileStorage는 주문 기록 파일을 로컬 디스크에서 읽습니다 — trader/replayengine이
// 기본으로 쓰는 위치(`./orders/`)와 같은 디렉터리를 가리키면 됩니다.
type LocalFileStorage struct {
	root string
}

// NewLocalFileStorage는 root 디렉터리 아래에서 파일을 읽는 Storage를 반환합니다.
func NewLocalFileStorage(root string) Storage {
	return &LocalFileStorage{root: root}
}

func (s *LocalFileStorage) Load(market string, start, end time.Time) ([]RecordedOrder, error) {
	path := filepath.Join(s.root, filepath.FromSlash(objectKey(market, start, end)))

	body, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("파일 읽기 실패: %w", err)
	}

	orders, err := decodeOrderRecordFile(body)
	if err != nil {
		return nil, fmt.Errorf("JSON 파싱 실패: %w", err)
	}
	return orders, nil
}

func (s *LocalFileStorage) ListDates() ([]string, error) {
	dates := make(map[string]bool)
	err := filepath.WalkDir(s.root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), "_orders.json") {
			return nil
		}
		if date, ok := parseDateFromObjectKey(d.Name()); ok {
			dates[date] = true
		}
		return nil
	})
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil // orders 디렉터리가 아직 없음 — 한 번도 기록된 적 없는 정상 상태
	}
	if err != nil {
		return nil, fmt.Errorf("주문 기록 디렉터리 조회 실패: %w", err)
	}
	return sortedDatesDesc(dates), nil
}
