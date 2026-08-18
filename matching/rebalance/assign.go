// Package rebalance은 FR-11(마켓 재분배)의 배정 로직을 담습니다 — "인스턴스가
// 늘거나 줄 때 20개 마켓을 물량에 비례해서 나눈다"는 부분을 Kafka/Redis와 무관한
// 순수 함수로 분리해뒀습니다(orderbook/match.go가 Kafka를 모르는 것과 같은 이유).
package rebalance

import "sort"

// Load는 한 마켓의 최근 추정 부하입니다(단위는 상대적 — 예: 최근 측정 구간의
// 초당 처리 건수 추정치). 절대값이 아니라 마켓들 사이의 상대 순서만 의미가 있습니다.
type Load struct {
	Market string
	Value  float64
}

// Assign은 LPT(Longest-Processing-Time-First) 그리디로 마켓을 멤버(인스턴스)들에
// 배정합니다 — 물량 내림차순으로 정렬한 뒤, 매 마켓을 "지금까지 누적 부하가 가장
// 적은 멤버"에 배정합니다. 동일 부하는 마켓명으로 결정적으로 타이브레이크합니다 —
// 이 함수는 Kafka가 그룹의 리더로 뽑은 인스턴스 하나만 실제로 호출하는데, 리더는
// 리밸런스마다 바뀔 수 있어서 "누가 실행하든 같은 입력엔 같은 결과"가 나와야
// 디버깅·재현이 가능합니다.
//
// 매번 이전 배정과 무관하게 처음부터 다시 계산합니다 — "이전 배정에서 최소한만
// 옮기기" 최적화는 하지 않습니다. 이 프로젝트 규모(마켓 20개, 인스턴스 몇 대)에서는
// 멤버십이 바뀔 때 마켓 하나가 추가로 옮겨가는 비용이 이미 만들어둔 FR-08 스냅샷
// 기반 복구로 충분히 감당되므로, 지금 단계에서는 단순함을 우선합니다 — 실측으로
// 문제가 확인되면 그때 "이전 배정을 시드로 쓰는" 안정화 변형을 추가합니다.
func Assign(loads []Load, members []string) map[string]string {
	assignment := make(map[string]string, len(loads))
	if len(members) == 0 {
		return assignment
	}

	sorted := make([]Load, len(loads))
	copy(sorted, loads)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Value != sorted[j].Value {
			return sorted[i].Value > sorted[j].Value
		}
		return sorted[i].Market < sorted[j].Market
	})

	memberOrder := make([]string, len(members))
	copy(memberOrder, members)
	sort.Strings(memberOrder)

	totals := make(map[string]float64, len(memberOrder))
	counts := make(map[string]int, len(memberOrder))
	for _, l := range sorted {
		least := memberOrder[0]
		for _, m := range memberOrder[1:] {
			// 부하 총량이 같으면(대표적으로 트래픽이 아직 없어 전부 0인 콜드 스타트)
			// 배정 개수가 적은 멤버로 타이브레이크합니다 — 안 그러면 totals가 절대
			// 안 바뀌어서(0+0=0) 이 루프가 항상 memberOrder[0]만 고르게 되고, 부하가
			// 전혀 없을 때 20개 마켓이 전부 한 인스턴스에 몰리는 결과가 됩니다(실제
			// 인스턴스 2개로 검증하다 관찰함) — "물량 기준, 동률이면 개수로 균등"이
			// 원래 의도한 동작입니다.
			if totals[m] < totals[least] || (totals[m] == totals[least] && counts[m] < counts[least]) {
				least = m
			}
		}
		assignment[l.Market] = least
		totals[least] += l.Value
		counts[least]++
	}
	return assignment
}
