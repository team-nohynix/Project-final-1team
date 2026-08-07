// Package validate는 주문 접수 요청의 유효성 검증(마켓/side/가격/수량)을 담당합니다.
package validate

import (
	"fmt"
	"math"
	"slices"
	"strconv"
)

// TargetMarkets는 업비트 원화 마켓 20개 목록입니다(requirements.md 1.1.4).
// backend/trader의 목록과 값은 같지만, 모듈 독립성 원칙에 따라 orderapi도 따로 선언합니다.
var TargetMarkets = []string{
	"KRW-USDT", "KRW-BTC", "KRW-XRP", "KRW-ETH", "KRW-ONDO",
	"KRW-LA", "KRW-SHIB", "KRW-RE", "KRW-DOGE", "KRW-SLX",
	"KRW-KAITO", "KRW-SOL", "KRW-XLM", "KRW-WLD", "KRW-MIRA",
	"KRW-ERA", "KRW-ADA", "KRW-AI", "KRW-NEAR", "KRW-ARX",
}

// IsTargetMarket은 market이 대상 20개 마켓에 포함되는지 확인합니다.
func IsTargetMarket(market string) bool {
	return slices.Contains(TargetMarkets, market)
}

// tickTiers는 업비트 원화 마켓의 가격대별 호가 단위 공개 규칙입니다
// (trader/order/ticksize.go와 같은 표 — 거기서는 반올림에, 여기서는 "배수인지 검증"에 씁니다).
var tickTiers = []struct {
	min  float64
	tick float64
}{
	{2_000_000, 1000},
	{1_000_000, 500},
	{500_000, 100},
	{100_000, 50},
	{10_000, 10},
	{1_000, 1},
	{100, 0.1},
	{10, 0.01},
	{1, 0.001},
	{0.1, 0.0001},
	{0.01, 0.00001},
	{0.001, 0.000001},
	{0.0001, 0.0000001},
	{0, 0.00000001},
}

func tickSizeFor(price float64) float64 {
	for _, t := range tickTiers {
		if price >= t.min {
			return t.tick
		}
	}
	return tickTiers[len(tickTiers)-1].tick
}

// isValidTickPrice는 price가 그 가격대의 호가 단위 배수인지 확인합니다.
// 부동소수점 표현 오차를 감안해 아주 작은 허용오차 안에서 비교합니다.
func isValidTickPrice(price float64) bool {
	tick := tickSizeFor(price)
	ratio := price / tick
	return math.Abs(ratio-math.Round(ratio)) < 1e-6
}

// ValidateSide는 side가 BUY/SELL인지 확인합니다.
func ValidateSide(side string) (errorCode, message string, ok bool) {
	if side != "BUY" && side != "SELL" {
		return "INVALID_SIDE", "side는 BUY 또는 SELL이어야 합니다.", false
	}
	return "", "", true
}

// ValidatePrice는 price 문자열을 파싱해 0보다 크고 호가 단위 배수인지 확인합니다.
func ValidatePrice(market, priceStr string) (price float64, errorCode, message string, ok bool) {
	price, err := strconv.ParseFloat(priceStr, 64)
	if err != nil || price <= 0 {
		return 0, "INVALID_PRICE", "가격이 0 이하이거나 형식 오류입니다.", false
	}
	if !isValidTickPrice(price) {
		tick := tickSizeFor(price)
		msg := fmt.Sprintf("가격이 %s의 호가 단위(%s원)의 배수가 아닙니다.", market, strconv.FormatFloat(tick, 'f', -1, 64))
		return 0, "INVALID_PRICE_UNIT", msg, false
	}
	return price, "", "", true
}

// ValidateQuantity는 quantity 문자열을 파싱해 0보다 큰지 확인합니다.
func ValidateQuantity(quantityStr string) (quantity float64, errorCode, message string, ok bool) {
	quantity, err := strconv.ParseFloat(quantityStr, 64)
	if err != nil || quantity <= 0 {
		return 0, "INVALID_QUANTITY", "수량이 0 이하이거나 형식 오류입니다.", false
	}
	return quantity, "", "", true
}
