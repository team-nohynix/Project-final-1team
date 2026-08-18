package bot

import (
	"math/rand"
	"time"
)

// 아래 상수들은 실측 데이터 없이 임의로 잡은 기본값입니다 — 나중에 조정 대상입니다.
const (
	noiseInterval    = 1 * time.Second
	noiseFireChance  = 0.3 // 매 tick마다 이 확률로만 주문 생성 (무작위스러운 빈도를 위함)
	noiseMinQuantity = 0.001
	noiseMaxQuantity = 0.01
)

// NoiseBot은 방향·수량 모두 무작위인 소량 주문을 간헐적으로 냅니다(FR-16).
// 판단 없이 배경 잡음(유동성 소비) 역할만 합니다.
type NoiseBot struct{}

func (NoiseBot) Name() string            { return "noise" }
func (NoiseBot) Interval() time.Duration { return noiseInterval }

func (NoiseBot) Decide(state *MarketState) []Decision {
	if rand.Float64() > noiseFireChance {
		return nil
	}

	price, ok := state.Price()
	if !ok {
		return nil
	}

	side := "BUY"
	if rand.Float64() < 0.5 {
		side = "SELL"
	}

	quantity := noiseMinQuantity + rand.Float64()*(noiseMaxQuantity-noiseMinQuantity)
	return []Decision{{Side: side, Price: price, Quantity: quantity}}
}
