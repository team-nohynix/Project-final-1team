package rebalance

import "testing"

func totalsByMember(loads []Load, assignment map[string]string) map[string]float64 {
	byMarket := make(map[string]float64, len(loads))
	for _, l := range loads {
		byMarket[l.Market] = l.Value
	}
	totals := make(map[string]float64)
	for market, member := range assignment {
		totals[member] += byMarket[market]
	}
	return totals
}

func TestAssignEvenLoadSplitsEvenlyByCount(t *testing.T) {
	loads := []Load{
		{"KRW-BTC", 10}, {"KRW-ETH", 10}, {"KRW-XRP", 10}, {"KRW-ADA", 10},
	}
	assignment := Assign(loads, []string{"i1", "i2"})

	counts := map[string]int{}
	for _, member := range assignment {
		counts[member]++
	}
	if counts["i1"] != 2 || counts["i2"] != 2 {
		t.Errorf("부하가 동일하면 마켓 개수도 2:2로 나뉘어야 하는데 %v", counts)
	}
}

// TestAssignZeroLoadSplitsEvenlyByCount는 실제 인스턴스 2개로 검증하다 발견한 버그의
// 회귀 테스트입니다 — 트래픽이 아직 없어 모든 마켓의 부하가 0(콜드 스타트)이면
// totals가 절대 바뀌지 않아서 "누적이 가장 적은 멤버"가 항상 정렬상 첫 멤버로
// 고정되고, 그 결과 20개 마켓이 전부 한 인스턴스에 몰렸다. 부하가 전부 같을 때는
// (0이든 아니든) 배정 개수로 균등하게 나뉘어야 한다.
func TestAssignZeroLoadSplitsEvenlyByCount(t *testing.T) {
	loads := make([]Load, 20)
	for i := range loads {
		loads[i] = Load{Market: string(rune('A' + i)), Value: 0}
	}
	assignment := Assign(loads, []string{"i1", "i2"})

	counts := map[string]int{}
	for _, member := range assignment {
		counts[member]++
	}
	if counts["i1"] != 10 || counts["i2"] != 10 {
		t.Errorf("부하가 전부 0이어도 개수는 10:10으로 나뉘어야 하는데 %v", counts)
	}
}

func TestAssignSkewedLoadBalancesByVolumeNotCount(t *testing.T) {
	// 마켓 하나(BTC=1000)가 나머지 4개(합계 40)보다 압도적으로 무거우면, LPT가
	// 낼 수 있는 최선은 "BTC 혼자 담당하는 인스턴스 + 나머지 4개를 몰아서 담당하는
	// 인스턴스"다(1000 vs 40을 2개로 쪼개는 것 자체가 이보다 더 균형 잡힐 수 없음).
	// 이 테스트가 확인하려는 건 "마켓 개수(1개 vs 4개)로 균등 분배하지 않는다"는
	// 것 — 개수 기준이었다면 BTC와 가벼운 마켓 하나를 묶어서 2:3으로 나눴을 것이다.
	loads := []Load{
		{"KRW-BTC", 1000},
		{"KRW-A", 10}, {"KRW-B", 10}, {"KRW-C", 10}, {"KRW-D", 10},
	}
	assignment := Assign(loads, []string{"i1", "i2"})
	totals := totalsByMember(loads, assignment)

	if assignment["KRW-BTC"] == assignment["KRW-A"] {
		t.Fatalf("가장 무거운 마켓(BTC)과 가벼운 마켓이 같은 인스턴스에 묶임(개수 기준으로 나눈 것처럼 보임): %+v", assignment)
	}
	// BTC를 담당하는 인스턴스가 나머지 4개를 담당하는 인스턴스보다 총 부하가
	// 낮아지면(=역전) 안 된다 — LPT가 "가장 가벼운 쪽에 넣는다"는 원칙 자체가 깨진 것.
	if totals[assignment["KRW-BTC"]] < totals[assignment["KRW-A"]] {
		t.Errorf("BTC 담당 인스턴스 총 부하(%v)가 나머지 담당 인스턴스(%v)보다 낮음: %+v", totals[assignment["KRW-BTC"]], totals[assignment["KRW-A"]], assignment)
	}
}

func TestAssignIsDeterministic(t *testing.T) {
	loads := []Load{
		{"KRW-BTC", 5}, {"KRW-ETH", 5}, {"KRW-XRP", 3}, {"KRW-ADA", 3}, {"KRW-SOL", 1},
	}
	members := []string{"i2", "i1", "i3"}

	first := Assign(loads, members)
	second := Assign(loads, members)

	if len(first) != len(second) {
		t.Fatalf("결과 크기가 다름: %d vs %d", len(first), len(second))
	}
	for market, member := range first {
		if second[market] != member {
			t.Errorf("%s: 1차 실행 %s, 2차 실행 %s — 같은 입력에 다른 결과", market, member, second[market])
		}
	}
}

func TestAssignAllMarketsCovered(t *testing.T) {
	loads := []Load{
		{"KRW-BTC", 5}, {"KRW-ETH", 3}, {"KRW-XRP", 1},
	}
	assignment := Assign(loads, []string{"i1", "i2", "i3"})

	if len(assignment) != len(loads) {
		t.Fatalf("배정 결과가 %d개, want %d개(마켓 전부 배정돼야 함)", len(assignment), len(loads))
	}
}

func TestAssignNoMembersReturnsEmpty(t *testing.T) {
	loads := []Load{{"KRW-BTC", 5}}
	assignment := Assign(loads, nil)
	if len(assignment) != 0 {
		t.Errorf("멤버가 없으면 배정도 없어야 하는데 %+v", assignment)
	}
}

func TestAssignSingleMemberGetsEverything(t *testing.T) {
	loads := []Load{{"KRW-BTC", 5}, {"KRW-ETH", 3}}
	assignment := Assign(loads, []string{"only"})
	for market, member := range assignment {
		if member != "only" {
			t.Errorf("%s -> %s, want only", market, member)
		}
	}
}
