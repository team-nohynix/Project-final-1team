package orderbook

import (
	"log"

	"github.com/shopspring/decimal"
)

// Apply는 incoming 주문을 반대편 호가와 매칭합니다(FR-06/FR-07). 가격-시간 우선 원칙에
// 따라 각 가격 레벨의 맨 앞(가장 먼저 접수된) 주문부터 체결하고, 체결가는 항상 먼저
// 호가창에 있던(선행) 주문의 가격을 따릅니다. 매칭 후 incoming에 수량이 남아 있으면
// 그 잔량을 새 미체결 주문으로 호가창에 편입합니다. 반환값은 이번 호출에서 발생한
// 체결들(하나도 없을 수 있음)이며, 발생 순서 그대로입니다.
//
// **중복 OrderID 방어 — 2026-08-27, 정합성 검사 "중복 체결" 사고의 진짜 원인.**
// 어떤 경로로든(컨슈머 재전달, 리밸런스 타이밍, 재시도 등) 같은 OrderID의 NEW
// 이벤트가 두 번 Apply되면, 이 체크가 없을 때는 완전히 새 *Order를 또 만들어
// insertResting이 호가창에 "같은 OrderID를 가진 별개의 리스트 노드"를 두 번째로
// 추가했다. elements 맵은 OrderID당 노드 하나만 가리킬 수 있어(나중 것으로
// 덮어써짐) 먼저 넣은 노드는 Cancel로도 다시는 못 지우는 유령 잔량이 되고,
// 이후 들어오는 모든 반대편 주문과 계속 체결되며 실제 수량을 몇 배로 초과
// 체결시켰다(실측: 주문 1건이 240번 체결돼 원래 수량의 1.7배 채워짐). 이 엔진은
// 마켓당 고루틴 하나로 순차 호출되는 게 전제라 여기서 락 없이 elements 맵만
// 확인하면 충분하다 — OrderID가 이미 호가창에 있으면(=이미 처리된 NEW) 이번
// 호출은 매칭도 편입도 하지 않고 완전히 무시한다.
func (ob *OrderBook) Apply(incoming *Order) []Execution {
	if _, exists := ob.elements[incoming.OrderID]; exists {
		log.Printf("[진단] Apply 중복 방어 발동 (market=%s orderId=%s bookPtr=%p) — 매칭 건너뜀", ob.Market, incoming.OrderID, ob)
		return nil
	}
	incoming.Price = normalizePrice(incoming.Price)

	oppositeSide := Sell
	crosses := func(restingPrice decimal.Decimal) bool { return restingPrice.LessThanOrEqual(incoming.Price) }
	if incoming.Side == Sell {
		oppositeSide = Buy
		crosses = func(restingPrice decimal.Decimal) bool { return restingPrice.GreaterThanOrEqual(incoming.Price) }
	}

	executions := ob.matchIncoming(incoming, oppositeSide, crosses)

	if incoming.Quantity.GreaterThan(decimal.Zero) {
		ob.insertResting(incoming)
	}
	return executions
}

// matchIncoming은 oppositeSide(들어온 주문과 반대편)의 최우선 가격부터 crosses가 참인
// 동안 계속 체결합니다. 레벨이 다 소진되면 그 레벨을 지우고, 항상 "현재" 최우선 가격
// (prices[0])을 다시 읽기 때문에 레벨 제거로 슬라이스 인덱스가 앞으로 밀려도 안전합니다.
func (ob *OrderBook) matchIncoming(incoming *Order, oppositeSide Side, crosses func(decimal.Decimal) bool) []Execution {
	var executions []Execution
	prices := ob.pricesFor(oppositeSide)

	for incoming.Quantity.GreaterThan(decimal.Zero) && len(*prices) > 0 {
		bestPrice := (*prices)[0]
		if !crosses(bestPrice) {
			break
		}

		levels := ob.levelsFor(oppositeSide)
		level := levels[bestPrice.String()]

		for incoming.Quantity.GreaterThan(decimal.Zero) && level.orders.Len() > 0 {
			front := level.orders.Front()
			resting := front.Value.(*Order)

			fillQty := minDecimal(incoming.Quantity, resting.Quantity)
			incoming.Quantity = incoming.Quantity.Sub(fillQty)
			resting.Quantity = resting.Quantity.Sub(fillQty)

			// **임시 진단 로깅(2026-08-27) — 다섯 번의 수정으로도 중복 체결이 계속
			// 재현돼(38→10→24→20→946건, 오히려 악화) 원인을 코드 리뷰만으로 못
			// 찾아서 실제 런타임 동작을 직접 관찰하기 위한 계측. resting 포인터
			// 주소(%p)로 "같은 유령 노드가 반복 체결되는지" vs "다른 메커니즘인지"를
			// 구분한다. 원인 확정되면 제거.
			ob.matchCounts[resting.OrderID]++
			if ob.matchCounts[resting.OrderID] == 5 || ob.matchCounts[resting.OrderID]%50 == 0 {
				log.Printf("[진단] resting 반복 체결 감지 (market=%s orderId=%s ptr=%p 누적체결=%d 남은수량=%s incoming=%s)",
					ob.Market, resting.OrderID, resting, ob.matchCounts[resting.OrderID], resting.Quantity, incoming.OrderID)
			}

			exec := Execution{Market: ob.Market, Price: resting.Price, Quantity: fillQty}
			if incoming.Side == Buy {
				exec.BuyOrderID, exec.SellOrderID = incoming.OrderID, resting.OrderID
			} else {
				exec.BuyOrderID, exec.SellOrderID = resting.OrderID, incoming.OrderID
			}
			executions = append(executions, exec)

			if resting.Quantity.IsZero() {
				level.orders.Remove(front)
				delete(ob.elements, resting.OrderID)
			}
		}
		ob.removeLevelIfEmpty(oppositeSide, bestPrice)
	}
	return executions
}
