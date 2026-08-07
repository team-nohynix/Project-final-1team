package main

import (
	"context"
	"log"
	"time"

	"replayengine/orderstore"
)

// Submitter는 재생 중 발생하는 제출을 어딘가로 보냅니다 — 테스트에서는 가짜 구현체를
// 주입할 수 있게 인터페이스로 뺐습니다(trader/order.OrderSubmitter와 같은 이유).
type Submitter interface {
	Submit(ctx context.Context, market string, o orderstore.RecordedOrder) error
}

// filterRange는 FR-27(구간 지정)의 기본 구현입니다 — fromTS/toTS가 0이면(플래그
// 미지정) 그 경계는 무시합니다. orders는 이미 ts 오름차순으로 기록돼 있다고
// 가정합니다(trader/order.InMemoryRecorder.Snapshot이 그렇게 정렬해서 저장함).
func filterRange(orders []orderstore.RecordedOrder, fromTS, toTS int64) []orderstore.RecordedOrder {
	if fromTS == 0 && toTS == 0 {
		return orders
	}
	out := make([]orderstore.RecordedOrder, 0, len(orders))
	for _, o := range orders {
		if fromTS != 0 && o.TS < fromTS {
			continue
		}
		if toTS != 0 && o.TS > toTS {
			continue
		}
		out = append(out, o)
	}
	return out
}

// replayMarket은 한 마켓의 기록된 주문들을 ts 간격에 맞춰(배속 적용) 순서대로
// 다시 제출합니다 — trader/replay/replay.go의 페이싱 루프와 동일한 공식이며,
// 차이는 판단 로직이 전혀 없다는 것뿐입니다(FR-18: "판단 로직 재실행 없이 그대로 재생").
func replayMarket(ctx context.Context, market string, orders []orderstore.RecordedOrder, speed float64, submitter Submitter) {
	var lastTS int64
	for i, o := range orders {
		if i > 0 {
			gap := time.Duration(o.TS-lastTS) * time.Millisecond
			if wait := time.Duration(float64(gap) / speed); wait > 0 {
				select {
				case <-time.After(wait):
				case <-ctx.Done():
					return
				}
			}
		}
		lastTS = o.TS

		if err := submitter.Submit(ctx, market, o); err != nil {
			log.Printf("[%s] 주문 재제출 실패: %v", market, err)
		}
	}
	log.Printf("[%s] 리플레이 완료 (%d건)", market, len(orders))
}
