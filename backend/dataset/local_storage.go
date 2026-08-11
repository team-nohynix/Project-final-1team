package dataset

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"backend/upbit"
)

// localStorage는 JSON 파일을 로컬 디스크에 저장합니다 (dev 환경 기본값).
// 경로 구조는 S3 키 레이아웃({market}/{start}_{end}_{batch|stream}.json)과 동일하게 맞춰,
// 나중에 S3로 전환해도 상대적인 구조가 그대로 대응됩니다.
type localStorage struct {
	root string
}

// NewLocalStorage는 root 디렉터리 아래에 파일을 저장하는 Storage를 반환합니다.
func NewLocalStorage(root string) Storage {
	return &localStorage{root: root}
}

func (s *localStorage) SaveBatch(b BatchFile, start, end time.Time) (string, error) {
	return s.writeJSON(b, b.Market, start, end, "batch")
}

func (s *localStorage) SaveStream(st StreamFile, start, end time.Time) (string, error) {
	return s.writeJSON(st, st.Market, start, end, "stream")
}

func (s *localStorage) LoadBatch(market string, start, end time.Time) (BatchFile, error) {
	var b BatchFile
	err := s.readJSON(market, start, end, "batch", &b)
	return b, err
}

func (s *localStorage) LoadStream(market string, start, end time.Time) (StreamFile, error) {
	var st StreamFile
	err := s.readJSON(market, start, end, "stream", &st)
	return st, err
}

func (s *localStorage) writeJSON(v any, market string, start, end time.Time, kind string) (string, error) {
	path := s.filePath(market, start, end, kind)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return "", fmt.Errorf("디렉터리 생성 실패: %w", err)
	}

	bytes, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "", fmt.Errorf("JSON 직렬화 실패: %w", err)
	}

	if err := os.WriteFile(path, bytes, 0644); err != nil {
		return "", fmt.Errorf("파일 쓰기 실패: %w", err)
	}

	return path, nil
}

func (s *localStorage) readJSON(market string, start, end time.Time, kind string, v any) error {
	path := s.filePath(market, start, end, kind)

	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return ErrNotFound
		}
		return fmt.Errorf("파일 읽기 실패: %w", err)
	}

	if err := json.Unmarshal(raw, v); err != nil {
		return fmt.Errorf("JSON 파싱 실패: %w", err)
	}

	return nil
}

// filePath는 SaveXxx/LoadXxx가 공유하는 경로 생성 규칙입니다.
func (s *localStorage) filePath(market string, start, end time.Time, kind string) string {
	filename := fmt.Sprintf("%s_%s_%s.json", formatFileTime(start), formatFileTime(end), kind)
	return filepath.Join(s.root, market, filename)
}

// formatFileTime은 파일명에 안전한(콜론 없는) 시각 포맷을 KST 기준으로 반환합니다.
// KST로 표시하는 이유: 팀 결정으로 날짜 경계 자체를 KST로 맞췄으니(server.go의
// parseDate), 파일명도 요청한 날짜와 시각적으로 일치해야 함 (7/27 요청이 UTC로
// 찍히면 파일명이 26일 15시로 보여 헷갈림). "Z"(UTC 표기) 접미사는 KST 표시에
// 안 맞아서 뺐습니다 — 콜론 없는 KST 로컬 시각이라는 뜻입니다.
func formatFileTime(t time.Time) string {
	return t.In(upbit.KST).Format("20060102T150405")
}
