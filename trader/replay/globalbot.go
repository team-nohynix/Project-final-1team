package replay

import (
	"context"
	"log"
	"sync"
	"time"

	"trader/bot"
	"trader/order"
)

// bedrockCallTimeout은 Decide 한 번(=Bedrock Converse 호출 한 번)에 주는 최대
// 시간입니다. 이게 없으면 네트워크가 멈춘 것 같은 상황에서 Decide가 영원히
// 안 끝나 이 봇의 티커가 다시는 못 돌게 됩니다(ctx.Done()으로도 못 빠져나옴).
const bedrockCallTimeout = 30 * time.Second

// globalBots는 20개 마켓을 한 번에 조망하며 판단하는 봇입니다(AI 트레이더, FR-16).
// client는 두 봇이 공유하는 실제 Bedrock 호출 경로입니다 — 전략(시스템 프롬프트)만
// 다르고 호출 방식은 같아서, 각 봇 구조체에 같은 client를 주입합니다.
func globalBots(client bot.BedrockClient) []bot.GlobalBot {
	return []bot.GlobalBot{
		bot.MomentumAIBot{Client: client},
		bot.MeanReversionAIBot{Client: client},
	}
}

// RunGlobalBots는 globalBots를 각자 판단 주기로 돌립니다. ctx가 취소되면(전체 마켓
// 재생 종료) 멈춥니다. states는 main.go가 마켓별로 미리 만들어 각 ReplayMarket과
// 공유하는 것과 동일한 맵으로, 여기서는 읽기만 합니다.
func RunGlobalBots(ctx context.Context, states map[string]*bot.MarketState, submitter order.OrderSubmitter, client bot.BedrockClient) {
	var wg sync.WaitGroup
	for _, b := range globalBots(client) {
		wg.Go(func() {
			runGlobalBot(ctx, b, states, submitter)
		})
	}
	wg.Wait()
}

func runGlobalBot(ctx context.Context, b bot.GlobalBot, states map[string]*bot.MarketState, submitter order.OrderSubmitter) {
	// 다른 봇들과 달리 배속(speed)으로 주기를 나누지 않습니다 — 실제 Bedrock 호출
	// 지연은 리플레이를 얼마나 빨리 돌리든 그대로이기 때문입니다(고정 실주기).
	// b.Decide()는 아래 select 루프 안에서 동기로 호출하는데, time.Ticker는 채널
	// 버퍼가 1개뿐이라 리시버(이 루프)가 Decide()로 바쁜 동안 지나간 틱은 그냥
	// 버려집니다 — 그래서 "이전 호출이 아직 진행 중이면 이번 틱은 스킵"이 별도
	// 잠금/플래그 없이 이 구조만으로 그대로 성립합니다.
	interval := b.Interval()
	if interval <= 0 {
		interval = time.Millisecond
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			callCtx, cancel := context.WithTimeout(ctx, bedrockCallTimeout)
			decisions := b.Decide(callCtx, states)
			cancel()
			for _, gd := range decisions {
				if _, err := submitter.Submit(ctx, order.NewOrder(gd.Market, gd.Decision)); err != nil {
					log.Printf("[global] %s 주문 제출 실패 (%s): %v", b.Name(), gd.Market, err)
				}
			}
		}
	}
}
