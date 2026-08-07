package archive

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// LocalStore는 마이크로배치를 로컬 디스크에 JSON 파일로 저장합니다 —
// trader/orderstore.LocalFileStorage와 같은 역할(ARCHIVE_BUCKET이 비어있을 때
// 기본값으로 씀).
type LocalStore struct {
	root string
}

func NewLocalStore(root string) *LocalStore {
	return &LocalStore{root: root}
}

func (s *LocalStore) Save(kind string, records []any) (string, error) {
	key := objectKey(kind)
	path := filepath.Join(s.root, filepath.FromSlash(key))
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return "", fmt.Errorf("디렉터리 생성 실패: %w", err)
	}

	body, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		return "", fmt.Errorf("JSON 직렬화 실패: %w", err)
	}

	if err := os.WriteFile(path, body, 0644); err != nil {
		return "", fmt.Errorf("파일 쓰기 실패: %w", err)
	}
	return path, nil
}

// objectKey는 호출마다 항상 새로운 키를 만듭니다({kind}/{시각}_{나노초}.json) —
// 같은 배치가 재실행으로 같은 키를 다시 가리킬 일이 없어서, backend/dataset의
// HeadObject 사전 확인 같은 idempotency 체크가 필요 없습니다(트레이더의
// 재현 가능한 파일 키와 다른 점).
func objectKey(kind string) string {
	now := time.Now().UTC()
	return fmt.Sprintf("%s/%s_%d.json", kind, now.Format("20060102T150405Z"), now.UnixNano())
}
