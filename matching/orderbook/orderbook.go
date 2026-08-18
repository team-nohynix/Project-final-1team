// Package orderbook은 매칭 엔진의 핵심 로직(호가창 자료구조 + 매칭 알고리즘)을 담습니다.
// Kafka나 Redis를 전혀 모르는 순수 로직이라, engine 패키지가 이 위에서 입출력을 담당합니다.
package orderbook

import (
	"container/list"
	"sort"

	"github.com/shopspring/decimal"
)

// Side는 주문의 매수/매도 방향입니다.
type Side string

const (
	Buy  Side = "BUY"
	Sell Side = "SELL"
)

// Order는 호가창에 들어있는(또는 막 들어온) 주문입니다. Quantity는 남은(미체결) 수량이고,
// Offset은 이 주문을 소비한 Kafka orders 토픽 파티션 안의 오프셋입니다 — 같은 가격일 때
// 시간 우선순위 판단 기준(FR-06)이자, 재시작 복구 체크포인트(engine 패키지, FR-08)에도 그대로
// 재사용합니다. 타임스탬프를 쓰지 않는 이유: 초당 수천 건 처리 시 같은 밀리초에 여러 주문이
// 들어올 수 있어 순서를 못 가르지만, offset은 파티션 안에서 항상 엄격한 전순서입니다.
type Order struct {
	OrderID  string
	Market   string
	Side     Side
	Price    decimal.Decimal
	Quantity decimal.Decimal
	Offset   int64
}

// Execution은 매칭 한 번으로 발생한 체결입니다. Price는 체결가로, 항상 먼저 호가창에
// 있던(선행) 주문의 가격을 따릅니다(FR-06).
type Execution struct {
	BuyOrderID  string
	SellOrderID string
	Market      string
	Price       decimal.Decimal
	Quantity    decimal.Decimal
}

// LevelView는 조회(FR-12)를 위한 가격 레벨의 스냅샷 — 그 가격에 걸린 총 잔량만 담습니다.
type LevelView struct {
	Price    decimal.Decimal
	Quantity decimal.Decimal
}

// priceLevel은 같은 가격의 미체결 주문들을 시간순(FIFO)으로 담습니다.
type priceLevel struct {
	price  decimal.Decimal
	orders *list.List // 원소 타입은 *Order, 맨 앞이 가장 오래된(선행) 주문
}

// OrderBook은 한 마켓의 매수·매도 호가창입니다. 동시성 제어는 이 타입의 책임이 아닙니다 —
// engine 패키지가 마켓당 고루틴 하나로 순차 호출하는 걸 전제로 락 없이 구현했습니다.
type OrderBook struct {
	Market string

	bidLevels map[string]*priceLevel // 정규화된 가격 문자열 -> 레벨
	askLevels map[string]*priceLevel

	bidPrices []decimal.Decimal // 활성 매수가만, 내림차순(높은 가격이 앞)
	askPrices []decimal.Decimal // 활성 매도가만, 오름차순(낮은 가격이 앞)

	elements map[string]*list.Element // orderID -> 그 주문이 들어있는 리스트 원소 (O(1) 취소)
}

// New는 빈 호가창을 만듭니다.
func New(market string) *OrderBook {
	return &OrderBook{
		Market:    market,
		bidLevels: make(map[string]*priceLevel),
		askLevels: make(map[string]*priceLevel),
		elements:  make(map[string]*list.Element),
	}
}

// normalizePrice는 가격을 소수점 8자리(업비트 최소 호가 단위, trader/order/ticksize.go의
// 마지막 tier와 동일)로 고정해, 같은 값이 문자열 표현 차이(예: "100" vs "100.00000000")로
// 서로 다른 가격 레벨로 갈리는 걸 막습니다. 맵 키로 쓰기 전 항상 이 함수를 거칩니다.
func normalizePrice(p decimal.Decimal) decimal.Decimal {
	return p.Truncate(8)
}

func minDecimal(a, b decimal.Decimal) decimal.Decimal {
	if a.LessThan(b) {
		return a
	}
	return b
}

func (ob *OrderBook) pricesFor(side Side) *[]decimal.Decimal {
	if side == Buy {
		return &ob.bidPrices
	}
	return &ob.askPrices
}

func (ob *OrderBook) levelsFor(side Side) map[string]*priceLevel {
	if side == Buy {
		return ob.bidLevels
	}
	return ob.askLevels
}

// insertResting은 o를 미체결 주문으로 호가창에 편입합니다(매칭 뒤 남은 수량이 있을 때,
// 또는 매칭 없이 그대로 접수될 때).
func (ob *OrderBook) insertResting(o *Order) {
	price := normalizePrice(o.Price)
	o.Price = price
	key := price.String()

	levels := ob.levelsFor(o.Side)
	level, ok := levels[key]
	if !ok {
		level = &priceLevel{price: price, orders: list.New()}
		levels[key] = level
		insertSortedPrice(ob.pricesFor(o.Side), price, o.Side)
	}
	elem := level.orders.PushBack(o)
	ob.elements[o.OrderID] = elem
}

// insertSortedPrice는 price를 정렬 순서를 유지하며 prices에 끼워 넣습니다
// (매수는 내림차순, 매도는 오름차순 — FR-05 검증 기준).
func insertSortedPrice(prices *[]decimal.Decimal, price decimal.Decimal, side Side) {
	idx := sort.Search(len(*prices), func(i int) bool {
		if side == Buy {
			return (*prices)[i].LessThan(price)
		}
		return (*prices)[i].GreaterThan(price)
	})
	*prices = append(*prices, decimal.Zero)
	copy((*prices)[idx+1:], (*prices)[idx:])
	(*prices)[idx] = price
}

// removeLevelIfEmpty는 레벨에 주문이 하나도 안 남았으면 맵과 정렬된 가격 목록에서 제거합니다.
func (ob *OrderBook) removeLevelIfEmpty(side Side, price decimal.Decimal) {
	key := price.String()
	levels := ob.levelsFor(side)
	level, ok := levels[key]
	if !ok || level.orders.Len() > 0 {
		return
	}
	delete(levels, key)

	prices := ob.pricesFor(side)
	for i, p := range *prices {
		if p.Equal(price) {
			*prices = append((*prices)[:i], (*prices)[i+1:]...)
			break
		}
	}
}

// Restore는 이미 매칭 판단이 끝난 미체결 주문을 그대로 호가창에 편입합니다 — insertResting과
// 동작은 같지만, "정상 접수(Apply, 매칭을 시도함)"와 "복구(이미 미체결 상태였던 걸 그대로
// 되돌림)"를 이름으로 구분하기 위한 공개 진입점입니다(engine 패키지의 FR-08 복구 전용).
func (ob *OrderBook) Restore(o *Order) {
	ob.insertResting(o)
}

// AllOrders는 side의 모든 미체결 주문을 레벨 순서 그대로(레벨 안에서는 FIFO 순서 그대로)
// 반환합니다 — 스냅샷 저장(FR-08/FR-12)을 위한 것으로, 이 순서 그대로 Restore를 반복
// 호출하면 동일한 상태가 재구성됩니다.
func (ob *OrderBook) AllOrders(side Side) []*Order {
	prices := *ob.pricesFor(side)
	levels := ob.levelsFor(side)
	var out []*Order
	for _, p := range prices {
		level := levels[p.String()]
		for e := level.orders.Front(); e != nil; e = e.Next() {
			out = append(out, e.Value.(*Order))
		}
	}
	return out
}

// Cancel은 orderID의 미체결 주문을 호가창에서 제거합니다(FR-10). 이미 없는(전량 체결됐거나
// 이미 취소된) 주문이면 조용히 아무 일도 하지 않습니다 — 매칭 엔진 입장에서는 orderapi가
// 이미 확정한 상태 전이를 다시 검증할 필요가 없습니다.
func (ob *OrderBook) Cancel(orderID string) {
	elem, ok := ob.elements[orderID]
	if !ok {
		return
	}
	o := elem.Value.(*Order)
	delete(ob.elements, orderID)

	levels := ob.levelsFor(o.Side)
	if level, ok := levels[o.Price.String()]; ok {
		level.orders.Remove(elem)
		ob.removeLevelIfEmpty(o.Side, o.Price)
	}
}

// Snapshot은 매수·매도 각각 최대 depth단계까지의 호가창 뷰를 만듭니다(FR-12). 반환값은
// 그 시점의 복사본이라, 이후 OrderBook이 바뀌어도 영향받지 않습니다.
func (ob *OrderBook) Snapshot(depth int) (bids, asks []LevelView) {
	bids = snapshotSide(ob.bidLevels, ob.bidPrices, depth)
	asks = snapshotSide(ob.askLevels, ob.askPrices, depth)
	return
}

func snapshotSide(levels map[string]*priceLevel, prices []decimal.Decimal, depth int) []LevelView {
	n := min(depth, len(prices))
	out := make([]LevelView, 0, n)
	for i := range n {
		level := levels[prices[i].String()]
		total := decimal.Zero
		for e := level.orders.Front(); e != nil; e = e.Next() {
			total = total.Add(e.Value.(*Order).Quantity)
		}
		out = append(out, LevelView{Price: level.price, Quantity: total})
	}
	return out
}
