package order

import "math"

// tickTiers는 업비트 원화 마켓의 가격대별 호가 단위(tick size) 공개 규칙입니다.
// min이 큰 것부터 정렬돼 있고, tickTier는 위에서부터 훑어 price가 속하는 첫 구간을 씁니다.
// decimals는 그 구간의 tick을 소수점 오차 없이 문자열로 표현하는 데 필요한 자릿수입니다.
var tickTiers = []struct {
	min      float64
	tick     float64
	decimals int
}{
	{2_000_000, 1000, 0},
	{1_000_000, 500, 0},
	{500_000, 100, 0},
	{100_000, 50, 0},
	{10_000, 10, 0},
	{1_000, 1, 0},
	{100, 0.1, 1},
	{10, 0.01, 2},
	{1, 0.001, 3},
	{0.1, 0.0001, 4},
	{0.01, 0.00001, 5},
	{0.001, 0.000001, 6},
	{0.0001, 0.0000001, 7},
	{0, 0.00000001, 8},
}

// tickTier는 price가 속한 가격대의 호가 단위와, 그 단위를 표현하는 데 필요한 소수 자릿수를 반환합니다.
func tickTier(price float64) (tick float64, decimals int) {
	for _, t := range tickTiers {
		if price >= t.min {
			return t.tick, t.decimals
		}
	}
	last := tickTiers[len(tickTiers)-1]
	return last.tick, last.decimals
}

// RoundToTick은 price를 그 가격대의 호가 단위 배수 중 가장 가까운 값으로 반올림합니다.
// docs/api-specification.md의 INVALID_PRICE_UNIT 규칙("가격은 마켓별 호가 단위의 배수여야
// 함")을 만족시키기 위한 것으로, 봇이 스프레드 계산 등으로 만들어낸 임의의 실수 가격을
// 실제 주문 가능한 가격으로 스냅합니다. decimals는 부동소수점 표현 오차 없이 문자열로
// 포맷할 때 쓸 소수 자릿수입니다(NewOrder 참고).
func RoundToTick(price float64) (rounded float64, decimals int) {
	tick, decimals := tickTier(price)
	return math.Round(price/tick) * tick, decimals
}
