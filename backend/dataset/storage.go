package dataset

import (
	"errors"
	"time"
)

// ErrNotFound는 요청한 market+기간의 batch/stream 파일이 저장소에 없을 때 반환됩니다.
// 호출자(예: 개별 파일 GET 핸들러)는 이 에러를 온디맨드 수집 트리거 신호로 사용합니다.
var ErrNotFound = errors.New("파일을 찾을 수 없습니다")

// Storage는 batch/stream JSON을 어딘가에 저장하고 읽어오는 방법을 추상화합니다.
// dev 환경은 localStorage, prod 환경은 s3Storage를 씁니다 (환경 선택은 main.go에서).
type Storage interface {
	SaveBatch(b BatchFile, start, end time.Time) (string, error)
	SaveStream(s StreamFile, start, end time.Time) (string, error)
	LoadBatch(market string, start, end time.Time) (BatchFile, error)
	LoadStream(market string, start, end time.Time) (StreamFile, error)
}
