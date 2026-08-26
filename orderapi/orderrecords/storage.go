// Package orderrecords는 trader가 기록해둔 FR-17 주문 파일(replayengine이 재생할
// 바로 그 파일)을 읽어 "재생하면 몇 건이 나갈지" 미리 세어보는 용도입니다 —
// replayengine/orderstore(재생 시 실제로 읽는 쪽)의 읽기 전용 대응이지만, 카운트/
// 시간 범위 계산에만 쓰이므로 RecordedOrder는 ts 필드만 갖습니다(다른 필드는 미리보기
// 목적에 필요 없어 아예 안 실음 — Side/Price/Quantity/OrderID도 JSON엔 있지만 디코딩
// 대상에서 뺌). 모듈 간 타입 비공유 원칙에 따라 독립적으로 다시 구현하지만, 파일 경로
// 규칙(objectKey)과 JSON 모양은 trader/orderstore가 쓰는 것과 정확히 같아야 합니다.
package orderrecords

import (
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"
)

// ErrNotFound는 요청한 마켓+기간에 해당하는 주문 기록 파일이 없을 때 반환됩니다 —
// 그 마켓에 그 기간 동안 기록된 주문이 아예 없었던 경우로, 정상적인 상황입니다.
var ErrNotFound = errors.New("주문 기록 파일을 찾을 수 없음")

// kst는 한국 표준시(UTC+9)입니다 — backend/upbit.KST와 같은 이유로
// time.LoadLocation("Asia/Seoul") 대신 FixedZone을 씁니다. ListDates가 파일명의
// UTC 타임스탬프를 다시 KST 캘린더 날짜로 바꾸는 데 씁니다(trader/main.go 등이
// -date를 KST 캘린더 하루로 해석하는 것과 짝을 맞추기 위함, 2026-08-26).
var kst = time.FixedZone("KST", 9*60*60)

// RecordedOrder는 미리보기(건수 세기, 시간 범위 계산)에 필요한 ts 필드만 담습니다.
type RecordedOrder struct {
	TS int64 `json:"ts"`
}

// Storage는 한 마켓의 기록된 주문 목록을 읽어오는 방법을 추상화합니다.
type Storage interface {
	Load(market string, start, end time.Time) ([]RecordedOrder, error)
	// ListDates는 트레이딩 기록(주문 파일)이 하나라도 있는 날짜를 KST 캘린더
	// 기준(YYYY-MM-DD)으로, 중복 없이, 최신순으로 반환합니다 — 팀 요청
	// (2026-08-26)으로 리플레이 날짜 선택 화면이 "기록이 실제로 있는 날짜"만
	// 고를 수 있게 지원합니다. 마켓 하나라도 그 날짜에 파일이 있으면 포함됩니다
	// (리플레이는 기록 없는 마켓은 건너뛰고 진행하므로, 20개 마켓이 전부 있어야
	// 하는 건 아닙니다). 한 번도 기록된 적이 없으면 빈 슬라이스(에러 아님).
	ListDates() ([]string, error)
}

// parseDateFromObjectKey는 objectKey가 만든 "{market}/{start}_{end}_orders.json"
// 형태(또는 파일명만 있는 "{start}_{end}_orders.json")에서 start 부분을 파싱해
// KST 캘린더 날짜(YYYY-MM-DD)로 돌려줍니다. 형식이 예상과 다르면(다른 목적의
// 파일이 섞여 들어온 경우 등) ok=false로 조용히 건너뛸 수 있게 합니다.
func parseDateFromObjectKey(key string) (string, bool) {
	base := key
	if i := strings.LastIndex(key, "/"); i >= 0 {
		base = key[i+1:]
	}
	parts := strings.SplitN(base, "_", 2)
	if len(parts) < 2 {
		return "", false
	}
	t, err := time.Parse("20060102T150405Z", parts[0])
	if err != nil {
		return "", false
	}
	return t.In(kst).Format("2006-01-02"), true
}

// sortedDatesDesc는 "YYYY-MM-DD" 문자열 집합을 최신순으로 정렬합니다 — 이
// 포맷은 사전식 정렬이 곧 날짜순 정렬이라 별도 파싱 없이 문자열 비교만으로
// 충분합니다.
func sortedDatesDesc(dates map[string]bool) []string {
	out := make([]string, 0, len(dates))
	for d := range dates {
		out = append(out, d)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(out)))
	return out
}

// orderRecordFile은 trader/orderstore가 쓰는 JSON 파일과 같은 모양입니다(range 등
// 나머지 필드는 미리보기에 필요 없어 아예 안 실음 — orders 배열의 ts만 있으면 됨).
type orderRecordFile struct {
	Orders []RecordedOrder `json:"orders"`
}

func decodeOrderRecordFile(body []byte) ([]RecordedOrder, error) {
	var f orderRecordFile
	if err := json.Unmarshal(body, &f); err != nil {
		return nil, err
	}
	return f.Orders, nil
}

// objectKey/formatFileTime은 trader/orderstore/storage.go와 정확히 같은 규칙입니다 —
// 여기서 값이 달라지면 트레이더가 쓴 파일을 못 찾습니다.
func objectKey(market string, start, end time.Time) string {
	return market + "/" + formatFileTime(start) + "_" + formatFileTime(end) + "_orders.json"
}

func formatFileTime(t time.Time) string {
	return t.UTC().Format("20060102T150405Z")
}
