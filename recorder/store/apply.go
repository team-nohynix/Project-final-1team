package store

import (
	"context"
	"fmt"
	"log"

	"recorder/events"
)

// ApplyOrderEvents는 orders 토픽에서 디코딩된 이벤트 배치를 Store에 반영합니다
// (RDS 백프레셔 대응 배칭, 2026-08-07 — CLAUDE.md 참고). NEW/CANCEL을 나눠서
// 각각 한 번의 배치 호출로 처리합니다 — 메시지 1건당 DB 왕복 1번이던 것을
// 배치 크기만큼 묶어서 왕복 횟수를 줄이는 게 목적입니다. evs가 1건이어도
// 정확히 같은 결과를 내므로, 배치가 안 찬 상황(트래픽이 적을 때)에도 문제없이
// 동작합니다.
func ApplyOrderEvents(ctx context.Context, s Store, evs []events.OrderEvent) error {
	news := make([]NewOrder, 0, len(evs))
	cancels := make([]CancelInput, 0, len(evs))
	for _, ev := range evs {
		switch ev.Type {
		case events.OrderNew:
			news = append(news, NewOrder{
				OrderID:         ev.OrderID,
				ClientRequestID: ev.ClientRequestID,
				Market:          ev.Market,
				Side:            ev.Side,
				Price:           ev.Price,
				Quantity:        ev.Quantity,
				Mode:            ev.Mode,
				SubmittedAt:     ev.AcceptedAt,
				SourceOrderID:   ev.SourceOrderID,
			})
		case events.OrderCancel:
			cancels = append(cancels, CancelInput{OrderID: ev.OrderID, CanceledAt: ev.CanceledAt})
		default:
			return fmt.Errorf("알 수 없는 이벤트 타입: %q", ev.Type)
		}
	}
	if len(news) > 0 {
		if err := s.InsertOrdersBatch(ctx, news); err != nil {
			return fmt.Errorf("주문 일괄 저장 실패 (%d건): %w", len(news), err)
		}
	}
	if len(cancels) > 0 {
		if err := s.CancelOrdersBatch(ctx, cancels); err != nil {
			return fmt.Errorf("취소 일괄 반영 실패 (%d건): %w", len(cancels), err)
		}
	}
	return nil
}

// ApplyExecutionEvents는 executions 토픽에서 디코딩된 이벤트 배치를 Store에
// 반영하고, Store가 보고한 결과(주문을 못 찾음/mode 불일치)를 항목별로 로그로
// 남깁니다. ApplyOrderEvents와 같은 배칭 목적입니다.
func ApplyExecutionEvents(ctx context.Context, s Store, evs []events.ExecutionEvent) error {
	if len(evs) == 0 {
		return nil
	}
	inputs := make([]ExecutionInput, len(evs))
	for i, ev := range evs {
		inputs[i] = ExecutionInput{
			Market:      ev.Market,
			BuyOrderID:  ev.BuyOrderID,
			SellOrderID: ev.SellOrderID,
			Price:       ev.Price,
			Quantity:    ev.Quantity,
		}
	}

	results, err := s.ApplyExecutionsBatch(ctx, inputs)
	if err != nil {
		return fmt.Errorf("체결 일괄 반영 실패 (%d건): %w", len(evs), err)
	}

	for i, result := range results {
		ev := evs[i]
		if !result.BuyFound {
			log.Printf("체결 반영 중 매수 주문을 찾지 못함 (orderId=%s) — execution은 그대로 기록됨", ev.BuyOrderID)
		}
		if !result.SellFound {
			log.Printf("체결 반영 중 매도 주문을 찾지 못함 (orderId=%s) — execution은 그대로 기록됨", ev.SellOrderID)
		}
		if result.ModeMismatched {
			log.Printf("체결 양쪽 주문의 mode가 다름 (buyOrderId=%s, sellOrderId=%s) — 매수측 mode(%s) 사용",
				ev.BuyOrderID, ev.SellOrderID, result.Mode)
		}
	}
	return nil
}

// ApplyAssignmentEvent는 assignments 토픽에서 디코딩된 이벤트 하나를 Store에
// 반영합니다(FR-11).
func ApplyAssignmentEvent(ctx context.Context, s Store, ev events.AssignmentEvent) error {
	in := AssignmentInput{Market: ev.Market, EngineInstanceID: ev.EngineInstanceID, At: ev.At}
	switch ev.Type {
	case events.AssignmentAssigned:
		return s.AssignMarket(ctx, in)
	case events.AssignmentReleased:
		return s.ReleaseMarket(ctx, in)
	default:
		return fmt.Errorf("알 수 없는 이벤트 타입: %q", ev.Type)
	}
}

// ResolveMode는 매수/매도 양쪽 trade_order.mode로부터 execution.mode를
// 결정합니다. 둘 다 있고 다르면 매수측이 이기고 mismatched=true를 반환합니다 —
// orderapi/session의 "동시 실행 방지" 덕분에 이 시스템 전체에서 PAPER_TRADING과
// REPLAY가 동시에 살아있을 수는 없으므로, 이 불일치는 "이전 세션이 남긴 미체결
// 주문이 새 세션의 주문과 매칭된" 드문 경우로 예상됩니다 — 조용히 추측하지 않고
// 크게 로그를 남기는 이유입니다. 한쪽만 있으면 그 값을 그대로 쓰고, 둘 다
// 없으면 빈 문자열을 반환합니다(두 주문 다 NEW를 못 본 극단적인 경우).
func ResolveMode(buyMode string, buyFound bool, sellMode string, sellFound bool) (mode string, mismatched bool) {
	switch {
	case buyFound && sellFound:
		return buyMode, buyMode != sellMode
	case buyFound:
		return buyMode, false
	case sellFound:
		return sellMode, false
	default:
		return "", false
	}
}
