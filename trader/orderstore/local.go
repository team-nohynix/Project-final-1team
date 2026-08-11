package orderstore

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"trader/order"
)

// LocalFileStorage는 주문 기록 파일을 로컬 디스크에 저장합니다 — 주문 기록용 S3 버킷이
// 준비되기 전 기본값이며, main.go에서 -order-bucket이 비어있을 때 선택됩니다.
type LocalFileStorage struct {
	root string
}

// NewLocalFileStorage는 root 디렉터리 아래에 파일을 저장하는 Storage를 반환합니다.
func NewLocalFileStorage(root string) Storage {
	return &LocalFileStorage{root: root}
}

func (s *LocalFileStorage) Save(market string, start, end time.Time, orders []order.RecordedOrder) (string, error) {
	path := filepath.Join(s.root, filepath.FromSlash(objectKey(market, start, end)))
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return "", fmt.Errorf("디렉터리 생성 실패: %w", err)
	}

	body, err := json.MarshalIndent(orderRecordFile{Market: market, Range: toRange(start, end), Orders: orders}, "", "  ")
	if err != nil {
		return "", fmt.Errorf("JSON 직렬화 실패: %w", err)
	}

	if err := os.WriteFile(path, body, 0644); err != nil {
		return "", fmt.Errorf("파일 쓰기 실패: %w", err)
	}
	return path, nil
}
