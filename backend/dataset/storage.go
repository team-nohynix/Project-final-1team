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
	// overwrite=false는 기존 파일이 있으면 재업로드 없이 그 경로를 그대로
	// 돌려주는 멱등 동작(putIfAbsent, 온디맨드 경로 — ensureMarketCollected가
	//씀)이고, overwrite=true는 기존 파일 유무와 무관하게 항상 방금 받아온
	// 데이터로 덮어씁니다(2026-08-26 추가 — "시세 수집 요청" 버튼을 다시
	// 눌러도 예전 캐시가 아니라 항상 최신 데이터를 받아오도록 해달라는 요청,
	// collectAllMarkets가 씀). localStorage는 원래도 항상 덮어쓰므로 이 값을
	// 무시합니다.
	SaveBatch(b BatchFile, start, end time.Time, overwrite bool) (string, error)
	SaveStream(s StreamFile, start, end time.Time, overwrite bool) (string, error)
	LoadBatch(market string, start, end time.Time) (BatchFile, error)
	LoadStream(market string, start, end time.Time) (StreamFile, error)
	// Exists는 market+[start,end) 기간의 batch/stream 파일이 이미 저장돼 있는지
	// 확인합니다 — Load와 달리 내용을 내려받지 않고 존재 여부만 봅니다(S3는
	// HeadObject, 로컬은 os.Stat). collector.go의 collectMarket이 업비트 API를
	// 부르기 전에 먼저 이걸로 확인해서, 이미 수집된 기간을 재요청해도 업비트
	// rate limit을 다시 쓰지 않게 하는 용도입니다(2026-08-19 추가 — 이전엔
	// putIfAbsent의 존재 확인이 저장 직전에만 있어서, Upbit 호출 자체는 항상
	// 다시 일어나는 문제가 있었습니다).
	Exists(market string, start, end time.Time) (ExistsResult, error)
}

// ExistsResult는 Exists의 결과입니다. 각 파일이 있으면 그 위치(URI/경로)도
// 같이 줘서, 호출부가 굳이 다시 Save를 부르지 않고도 CollectResult에 쓸 경로를
// 얻을 수 있게 합니다.
type ExistsResult struct {
	BatchExists  bool
	BatchPath    string
	StreamExists bool
	StreamPath   string
}

// AllExist는 batch/stream이 둘 다 있을 때만 true입니다 — 둘 중 하나만 있으면
// (예: 이전 수집이 batch 저장 후 stream 저장 전에 실패한 경우) 다시 수집해야
// 안전하므로 false로 취급합니다.
func (r ExistsResult) AllExist() bool {
	return r.BatchExists && r.StreamExists
}
