package orderbook

import "github.com/shopspring/decimal"

// Apply는 incoming 주문을 반대편 호가와 매칭합니다(FR-06/FR-07). 가격-시간 우선 원칙에
// 따라 각 가격 레벨의 맨 앞(가장 먼저 접수된) 주문부터 체결하고, 체결가는 항상 먼저
// 호가창에 있던(선행) 주문의 가격을 따릅니다. 매칭 후 incoming에 수량이 남아 있으면
// 그 잔량을 새 미체결 주문으로 호가창에 편입합니다. 반환값은 이번 호출에서 발생한
// 체결들(하나도 없을 수 있음)이며, 발생 순서 그대로입니다.
func (ob *OrderBook) Apply(incoming *Order) []Execution {
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
