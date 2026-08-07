// Package orderstore는 트레이더(FR-17)가 기록해둔 주문 파일을 읽어옵니다 —
// trader/orderstore(쓰기 전용)의 읽기 전용 대응입니다. 모듈 간 타입 비공유
// 원칙에 따라 독립적으로 다시 구현하지만, 파일 경로 규칙(objectKey)과 JSON
// 모양은 정확히 같아야 실제로 트레이더가 쓴 파일을 찾아 읽을 수 있습니다.
package orderstore

import (
	"encoding/json"
	"errors"
	"time"
)

// ErrNotFound는 요청한 마켓+기간에 해당하는 주문 기록 파일이 없을 때 반환됩니다 —
// 그 마켓에 그 기간 동안 기록된 주문이 아예 없었던 경우로, 정상적인 상황입니다.
var ErrNotFound = errors.New("주문 기록 파일을 찾을 수 없음")

// RecordedOrder는 trader/order.RecordedOrder와 필드가 정확히 같은 모양입니다.
// OrderID는 원본 페이퍼 트레이딩 주문의 orderId — submitter.go가 리플레이 주문을
// 다시 제출할 때 sourceOrderId로 실어 보내, 기록기가 TRADE_ORDER.source_order_id
// (docs/erd.md)를 채울 수 있게 합니다.
type RecordedOrder struct {
	TS       int64  `json:"ts"`
	Side     string `json:"side"`
	Price    string `json:"price"`
	Quantity string `json:"quantity"`
	OrderID  string `json:"orderId"`
}

// Storage는 한 마켓의 기록된 주문들을 읽어오는 방법을 추상화합니다.
type Storage interface {
	Load(market string, start, end time.Time) ([]RecordedOrder, error)
}

// orderRecordFile은 trader/orderstore가 쓰는 JSON 파일과 같은 모양입니다.
type orderRecordFile struct {
	Market string          `json:"market"`
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
