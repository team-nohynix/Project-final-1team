package bot

import (
	"math/rand"
	"time"
)

// 아래 상수들은 실측 데이터 없이 임의로 잡은 기본값입니다 — 나중에 조정 대상입니다.
const (
	largeOrderInterval    = 60 * time.Second
	largeOrderFireChance  = 0.2 // interval마다 이 확률로만 발동 (noise보다 훨씬 드물게)
	largeOrderMinQuantity = 0.5
	largeOrderMaxQuantity = 2.0
)

// LargeOrderBot은 평소엔 조용하다가 간헐적으로 훨씬 큰 수량의 주문을 냅니다(FR-16).
// 시세 급변/유동성 스트레스 상황을 인위적으로 만드는 역할입니다.
type LargeOrderBot struct{}

func (LargeOrderBot) Name() string            { return "large_order" }
func (LargeOrderBot) Interval() time.Duration { return largeOrderInterval }

func (LargeOrderBot) Decide(state *MarketState) []Decision {
	if rand.Float64() > largeOrderFireChance {
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

	quantity := largeOrderMinQuantity + rand.Float64()*(largeOrderMaxQuantity-largeOrderMinQuantity)
	return []Decision{{Side: side, Price: price, Quantity: quantity}}
}
