package orderstore

import (
	"encoding/json"
	"errors"
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

	combined, err := mergeWithExistingLocal(path, orders)
	if err != nil {
		return "", err
	}

	body, err := json.MarshalIndent(orderRecordFile{Market: market, Range: toRange(start, end), Orders: combined}, "", "  ")
	if err != nil {
		return "", fmt.Errorf("JSON 직렬화 실패: %w", err)
	}

	if err := os.WriteFile(path, body, 0644); err != nil {
		return "", fmt.Errorf("파일 쓰기 실패: %w", err)
	}
	return path, nil
}

// mergeWithExistingLocal은 path에 이미 저장된 주문 기록이 있으면 그 뒤에 orders를
// 이어붙입니다(Storage.Save의 병합 계약 참고). 파일이 아직 없으면(첫 플러시) orders를
// 그대로 돌려줍니다.
func mergeWithExistingLocal(path string, orders []order.RecordedOrder) ([]order.RecordedOrder, error) {
	existingBody, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return orders, nil
	}
	if err != nil {
		return nil, fmt.Errorf("기존 파일 읽기 실패: %w", err)
	}
	var existing orderRecordFile
	if err := json.Unmarshal(existingBody, &existing); err != nil {
		return nil, fmt.Errorf("기존 파일 파싱 실패: %w", err)
	}
	return append(existing.Orders, orders...), nil
}
