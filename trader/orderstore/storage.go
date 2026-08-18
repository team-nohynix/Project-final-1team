package orderstore

import (
	"time"

	"trader/order"
)

// Storage는 한 마켓에서 기록된 주문들(FR-17)을 어딘가에 저장하는 방법을 추상화합니다.
// 주문 기록용 S3 버킷이 준비되기 전에는 LocalFileStorage, 준비된 뒤에는 S3Storage를 씁니다.
type Storage interface {
	Save(market string, start, end time.Time, orders []order.RecordedOrder) (string, error)
}

// Range는 주문 기록 파일이 커버하는 기간입니다.
type Range struct {
	Start string `json:"start"`
	End   string `json:"end"`
}

// orderRecordFile은 저장되는 JSON 파일 하나의 모양입니다.
type orderRecordFile struct {
	Market string                `json:"market"`
	Range  Range                 `json:"range"`
	Orders []order.RecordedOrder `json:"orders"`
}

func toRange(start, end time.Time) Range {
	return Range{Start: start.UTC().Format(time.RFC3339), End: end.UTC().Format(time.RFC3339)}
}

// objectKey는 LocalFileStorage/S3Storage가 공유하는 경로/키 생성 규칙입니다.
// backend의 시세 데이터 키 레이아웃({market}/{start}_{end}_{batch|stream}.json)과
// 같은 관례를 따르되, 종류를 나타내는 접미사만 orders로 씁니다.
func objectKey(market string, start, end time.Time) string {
	return market + "/" + formatFileTime(start) + "_" + formatFileTime(end) + "_orders.json"
}

func formatFileTime(t time.Time) string {
	return t.UTC().Format("20060102T150405Z")
}
