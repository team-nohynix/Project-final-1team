package orderbook

import (
	"testing"

	"github.com/shopspring/decimal"
)

func d(s string) decimal.Decimal {
	v, err := decimal.NewFromString(s)
	if err != nil {
		panic(err)
	}
	return v
}

func order(id string, side Side, price, qty string, offset int64) *Order {
	return &Order{OrderID: id, Market: "KRW-BTC", Side: side, Price: d(price), Quantity: d(qty), Offset: offset}
}

// TestApplyNoMatchPriceGap은 매수가가 최우선 매도가보다 낮으면 체결이 안 되고 그대로
// 호가창에 남는지 확인합니다.
func TestApplyNoMatchPriceGap(t *testing.T) {
	ob := New("KRW-BTC")
	ob.Apply(order("ask1", Sell, "100", "1", 1))

	execs := ob.Apply(order("bid1", Buy, "99", "1", 2))
	if len(execs) != 0 {
		t.Fatalf("체결이 없어야 하는데 %d건 발생", len(execs))
	}

	bids, asks := ob.Snapshot(10)
	if len(bids) != 1 || len(asks) != 1 {
		t.Fatalf("매수/매도 모두 1건씩 남아있어야 함, bids=%v asks=%v", bids, asks)
	}
}

// TestApplyExactMatch는 수량이 정확히 같은 매수/매도가 만나면 완전 체결되고 양쪽 다
// 호가창에서 사라지는지 확인합니다. 체결가는 선행 주문(매도)의 가격을 따라야 합니다.
func TestApplyExactMatch(t *testing.T) {
	ob := New("KRW-BTC")
	ob.Apply(order("ask1", Sell, "100", "1", 1))

	execs := ob.Apply(order("bid1", Buy, "100", "1", 2))
	if len(execs) != 1 {
		t.Fatalf("체결 1건을 기대했으나 %d건", len(execs))
	}
	if !execs[0].Price.Equal(d("100")) {
		t.Errorf("체결가 = %s, want 100", execs[0].Price)
	}
	if execs[0].BuyOrderID != "bid1" || execs[0].SellOrderID != "ask1" {
		t.Errorf("체결 주문ID = buy=%s sell=%s", execs[0].BuyOrderID, execs[0].SellOrderID)
	}

	bids, asks := ob.Snapshot(10)
	if len(bids) != 0 || len(asks) != 0 {
		t.Fatalf("완전 체결 후 양쪽 다 비어야 함, bids=%v asks=%v", bids, asks)
	}
}

// TestApplyIncomingLargerThanResting는 들어온 주문이 선행 주문보다 커서, 선행 주문은
// 전량 소진되고 들어온 주문은 잔량이 남아 새 레벨로 편입되는지 확인합니다(FR-07).
func TestApplyIncomingLargerThanResting(t *testing.T) {
	ob := New("KRW-BTC")
	ob.Apply(order("ask1", Sell, "100", "1", 1))

	execs := ob.Apply(order("bid1", Buy, "100", "3", 2))
	if len(execs) != 1 || !execs[0].Quantity.Equal(d("1")) {
		t.Fatalf("체결 1건(수량 1)을 기대했으나 %+v", execs)
	}

	bids, asks := ob.Snapshot(10)
	if len(asks) != 0 {
		t.Fatalf("매도 잔량이 남아있으면 안 됨: %v", asks)
	}
	if len(bids) != 1 || !bids[0].Quantity.Equal(d("2")) {
		t.Fatalf("매수 잔량 2가 새 레벨로 남아야 함: %v", bids)
	}
}

// TestApplyRestingLargerThanIncoming는 선행 주문이 더 커서, 들어온 주문만 전량 소진되고
// 선행 주문은 잔량만 줄어든 채 호가창에 남는지 확인합니다(FR-07).
func TestApplyRestingLargerThanIncoming(t *testing.T) {
	ob := New("KRW-BTC")
	ob.Apply(order("ask1", Sell, "100", "5", 1))

	execs := ob.Apply(order("bid1", Buy, "100", "2", 2))
	if len(execs) != 1 || !execs[0].Quantity.Equal(d("2")) {
		t.Fatalf("체결 1건(수량 2)을 기대했으나 %+v", execs)
	}

	bids, asks := ob.Snapshot(10)
	if len(bids) != 0 {
		t.Fatalf("매수 잔량이 남아있으면 안 됨: %v", bids)
	}
	if len(asks) != 1 || !asks[0].Quantity.Equal(d("3")) {
		t.Fatalf("매도 잔량 3이 남아야 함: %v", asks)
	}
}

// TestApplyCrossesMultipleLevels는 들어온 주문 하나가 여러 매도 레벨을 낮은 가격부터
// 순서대로 체결시키는지 확인합니다.
func TestApplyCrossesMultipleLevels(t *testing.T) {
	ob := New("KRW-BTC")
	ob.Apply(order("ask1", Sell, "101", "1", 1))
	ob.Apply(order("ask2", Sell, "100", "1", 2)) // 더 싼 매도가 나중에 들어와도 먼저 체결돼야 함
	ob.Apply(order("ask3", Sell, "102", "1", 3))

	execs := ob.Apply(order("bid1", Buy, "102", "3", 4))
	if len(execs) != 3 {
		t.Fatalf("3개 레벨 전부 체결을 기대했으나 %d건", len(execs))
	}
	wantOrder := []string{"ask2", "ask1", "ask3"} // 100 -> 101 -> 102 순
	for i, e := range execs {
		if e.SellOrderID != wantOrder[i] {
			t.Errorf("체결 %d번째 = %s, want %s (낮은 가격부터)", i, e.SellOrderID, wantOrder[i])
		}
	}
}

// TestApplyFIFOAtSamePriceLevel는 같은 가격에 여러 주문이 있을 때 먼저 들어온 순서대로
// 체결되는지(시간 우선) 확인합니다.
func TestApplyFIFOAtSamePriceLevel(t *testing.T) {
	ob := New("KRW-BTC")
	ob.Apply(order("ask1", Sell, "100", "1", 1))
	ob.Apply(order("ask2", Sell, "100", "1", 2))
	ob.Apply(order("ask3", Sell, "100", "1", 3))

	execs := ob.Apply(order("bid1", Buy, "100", "2", 4))
	if len(execs) != 2 {
		t.Fatalf("체결 2건을 기대했으나 %d건", len(execs))
	}
	if execs[0].SellOrderID != "ask1" || execs[1].SellOrderID != "ask2" {
		t.Errorf("FIFO 순서 위반: %+v", execs)
	}

	_, asks := ob.Snapshot(10)
	if len(asks) != 1 || !asks[0].Quantity.Equal(d("1")) {
		t.Fatalf("ask3(수량 1)만 남아야 함: %v", asks)
	}
}

// TestCancelRemovesRestingOrder는 취소된 주문이 매칭 대상에서 빠지는지 확인합니다(FR-10).
func TestCancelRemovesRestingOrder(t *testing.T) {
	ob := New("KRW-BTC")
	ob.Apply(order("ask1", Sell, "100", "1", 1))
	ob.Cancel("ask1")

	execs := ob.Apply(order("bid1", Buy, "100", "1", 2))
	if len(execs) != 0 {
		t.Fatalf("취소된 주문과는 체결되면 안 되는데 %d건 발생", len(execs))
	}
	_, asks := ob.Snapshot(10)
	if len(asks) != 0 {
		t.Fatalf("취소 후 호가창에 남아있으면 안 됨: %v", asks)
	}
}

// TestCancelUnknownOrderIsNoop는 존재하지 않는(이미 체결됐거나 없는) 주문 ID를 취소해도
// 에러 없이 조용히 무시되는지 확인합니다.
func TestCancelUnknownOrderIsNoop(t *testing.T) {
	ob := New("KRW-BTC")
	ob.Cancel("no-such-order") // 패닉/에러 없이 통과하면 성공
}

// TestSnapshotSortOrder는 매수는 고가순, 매도는 저가순으로 정렬되는지 확인합니다(FR-05).
func TestSnapshotSortOrder(t *testing.T) {
	ob := New("KRW-BTC")
	ob.Apply(order("bid1", Buy, "100", "1", 1))
	ob.Apply(order("bid2", Buy, "102", "1", 2))
	ob.Apply(order("bid3", Buy, "101", "1", 3))
	ob.Apply(order("ask1", Sell, "200", "1", 4))
	ob.Apply(order("ask2", Sell, "198", "1", 5))
	ob.Apply(order("ask3", Sell, "199", "1", 6))

	bids, asks := ob.Snapshot(10)
	wantBids := []string{"102", "101", "100"}
	for i, lv := range bids {
		if lv.Price.String() != d(wantBids[i]).Truncate(8).String() {
			t.Errorf("bids[%d] = %s, want %s", i, lv.Price, wantBids[i])
		}
	}
	wantAsks := []string{"198", "199", "200"}
	for i, lv := range asks {
		if lv.Price.String() != d(wantAsks[i]).Truncate(8).String() {
			t.Errorf("asks[%d] = %s, want %s", i, lv.Price, wantAsks[i])
		}
	}
}

// TestSnapshotDepthLimit는 depth로 반환 레벨 수를 제한할 수 있는지 확인합니다.
func TestSnapshotDepthLimit(t *testing.T) {
	ob := New("KRW-BTC")
	for i := range 5 {
		ob.Apply(order("bid"+string(rune('1'+i)), Buy, "10"+string(rune('0'+i)), "1", int64(i)))
	}
	bids, _ := ob.Snapshot(2)
	if len(bids) != 2 {
		t.Fatalf("depth=2인데 %d건 반환", len(bids))
	}
}

// TestApplyPriceNormalizationSameLevel는 "100"과 "100.00000000"처럼 표현이 다른 같은
// 가격이 같은 레벨로 합쳐지는지 확인합니다(map 키 정규화, orderbook.go의 normalizePrice).
func TestApplyPriceNormalizationSameLevel(t *testing.T) {
	ob := New("KRW-BTC")
	ob.Apply(order("ask1", Sell, "100", "1", 1))
	ob.Apply(order("ask2", Sell, "100.00000000", "1", 2))

	_, asks := ob.Snapshot(10)
	if len(asks) != 1 {
		t.Fatalf("표현이 다른 같은 가격은 레벨 1개로 합쳐져야 하는데 %d개", len(asks))
	}
	if !asks[0].Quantity.Equal(d("2")) {
		t.Errorf("합쳐진 레벨의 총 수량 = %s, want 2", asks[0].Quantity)
	}
}

// TestApplyDeterministicReplay는 같은 이벤트 시퀀스를 두 번 재생했을 때 최종 호가창
// 상태가 동일한지 확인합니다 — 재시작 복구(FR-08)가 "같은 입력 -> 같은 결과"를
// 전제하므로, 이 성질이 깨지면 복구 자체가 신뢰할 수 없어집니다.
func TestApplyDeterministicReplay(t *testing.T) {
	events := []*Order{
		order("ask1", Sell, "100", "2", 1),
		order("ask2", Sell, "101", "3", 2),
		order("bid1", Buy, "101", "4", 3),
		order("bid2", Buy, "99", "1", 4),
	}

	replay := func() (bids, asks []LevelView) {
		ob := New("KRW-BTC")
		for _, e := range events {
			cp := *e // Apply가 Quantity를 소비하므로 매 replay마다 새 복사본을 넣음
			ob.Apply(&cp)
		}
		return ob.Snapshot(10)
	}

	bids1, asks1 := replay()
	bids2, asks2 := replay()

	if len(bids1) != len(bids2) || len(asks1) != len(asks2) {
		t.Fatalf("재생 결과 레벨 수가 다름: bids %d vs %d, asks %d vs %d", len(bids1), len(bids2), len(asks1), len(asks2))
	}
	for i := range bids1 {
		if !bids1[i].Price.Equal(bids2[i].Price) || !bids1[i].Quantity.Equal(bids2[i].Quantity) {
			t.Errorf("bids[%d] 불일치: %+v vs %+v", i, bids1[i], bids2[i])
		}
	}
	for i := range asks1 {
		if !asks1[i].Price.Equal(asks2[i].Price) || !asks1[i].Quantity.Equal(asks2[i].Quantity) {
			t.Errorf("asks[%d] 불일치: %+v vs %+v", i, asks1[i], asks2[i])
		}
	}
}
