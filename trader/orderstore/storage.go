package orderstore

import (
	"time"

	"trader/order"
)

// Storage는 한 마켓에서 기록된 주문들(FR-17)을 어딘가에 저장하는 방법을 추상화합니다.
// 주문 기록용 S3 버킷이 준비되기 전에는 LocalFileStorage, 준비된 뒤에는 S3Storage를 씁니다.
//
// Save는 reset에 따라 덮어쓰기 또는 병합입니다(2026-08-25) — main.go가 실행 도중
// 주기적으로(order.InMemoryRecorder.Drain으로 비운 만큼씩) Save를 여러 번 호출해
// 메모리 누적을 막는데, 같은 market+start+end 키를 쓰는 모든 저장이 무조건 병합이면
// 실전에서 실제로 터진 문제가 있었습니다 — 같은 날짜(-date)로 트레이더를 여러 번
// 재실행(테스트, 재시도 등)하면 그 서로 다른 실행들의 기록까지 한 파일에 계속
// 쌓여서, "그 날짜의 대표 기록 파일 하나"라는 전제가 깨지고 replayengine의 재생
// 대상/소요시간 추정이 완전히 틀어졌습니다(예: 실제론 하루치인데 여러 실행이 겹쳐
// 76시간으로 나옴). 그래서 reset=true(한 실행의 첫 플러시)는 기존 내용을 버리고
// 덮어쓰고, reset=false(같은 실행의 이후 플러시)만 병합합니다 — main.go가 마켓별로
// "이 실행에서 이미 한 번 저장했는지"를 추적해서 첫 호출만 reset=true로 부릅니다.
type Storage interface {
	Save(market string, start, end time.Time, orders []order.RecordedOrder, reset bool) (string, error)
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
