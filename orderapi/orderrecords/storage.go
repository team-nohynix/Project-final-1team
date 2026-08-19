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
	"time"
)

// ErrNotFound는 요청한 마켓+기간에 해당하는 주문 기록 파일이 없을 때 반환됩니다 —
// 그 마켓에 그 기간 동안 기록된 주문이 아예 없었던 경우로, 정상적인 상황입니다.
var ErrNotFound = errors.New("주문 기록 파일을 찾을 수 없음")

// RecordedOrder는 미리보기(건수 세기, 시간 범위 계산)에 필요한 ts 필드만 담습니다.
type RecordedOrder struct {
	TS int64 `json:"ts"`
}

// Storage는 한 마켓의 기록된 주문 목록을 읽어오는 방법을 추상화합니다.
type Storage interface {
	Load(market string, start, end time.Time) ([]RecordedOrder, error)
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
