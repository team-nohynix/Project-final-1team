package bot

import "time"

// 아래 상수들은 실측 데이터 없이 임의로 잡은 기본값입니다 — 나중에 조정 대상입니다.
const (
	marketMakerInterval = 2 * time.Second
	marketMakerSpread   = 0.002 // 현재가 대비 ±0.2%
	marketMakerQuantity = 0.01
)

// MarketMakerBot은 현재가 기준 스프레드를 두고 매수/매도 호가를 동시에, 상시로 걸어
// 호가창을 채우는 역할을 합니다(FR-16). 판단이랄 게 거의 없어 규칙만으로 구현합니다.
type MarketMakerBot struct{}

func (MarketMakerBot) Name() string            { return "market_maker" }
func (MarketMakerBot) Interval() time.Duration { return marketMakerInterval }

func (MarketMakerBot) Decide(state *MarketState) []Decision {
	price, ok := state.Price()
	if !ok {
		return nil
	}

	return []Decision{
		{Side: "BUY", Price: price * (1 - marketMakerSpread), Quantity: marketMakerQuantity},
		{Side: "SELL", Price: price * (1 + marketMakerSpread), Quantity: marketMakerQuantity},
	}
}
