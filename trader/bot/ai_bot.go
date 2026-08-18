package bot

import (
	"context"
	"log"
	"time"
)

// 아래 두 봇은 requirements.md FR-16의 모멘텀 추종/평균회귀 봇입니다.
// 팀 결정: 규칙 기반이 아니라 AWS Bedrock으로 판단하고, 마켓별로 따로 두지 않고
// "20개 마켓을 한 번에 조망해서 그중 어디를 얼마나 살지/팔지" 판단하는 전체 조망형
// (포트폴리오형)으로 만든다 — 비용 면에서도(호출 20회 대신 2회), "AI 트레이더"라는
// 이름에도 이 편이 더 맞다는 판단.
//
// 실제 Bedrock 호출은 BedrockClient(bedrock.go)에 위임합니다 — 두 봇은 호출 방식이
// 동일하고 전략 설명(시스템 프롬프트)만 다르므로, 호출 로직을 중복하지 않습니다.
const aiJudgeInterval = 5 * time.Second

const momentumSystemPrompt = `당신은 암호화폐 단기 모멘텀 추종(momentum) 트레이더입니다.
사용자 메시지에는 여러 마켓의 최근 가격 흐름이 오래된 순서로 담긴 JSON이 주어집니다.
최근 상승세가 뚜렷한 마켓이 있으면 소량 매수(BUY)를 판단하세요. 뚜렷한 상승세가 없는
마켓은 판단하지 않아도 됩니다. 판단이 없으면 decisions를 빈 배열로 제출하세요.`

const meanReversionSystemPrompt = `당신은 암호화폐 평균회귀(mean reversion) 트레이더입니다.
사용자 메시지에는 여러 마켓의 최근 가격 흐름이 오래된 순서로 담긴 JSON이 주어집니다.
최근 가격이 그 마켓 자체의 평균 대비 과도하게 오르거나 내린 마켓이 있으면, 되돌림을
기대하는 반대 방향(과열 시 SELL, 과매도 시 BUY)을 판단하세요. 뚜렷한 과열/과매도가
없는 마켓은 판단하지 않아도 됩니다. 판단이 없으면 decisions를 빈 배열로 제출하세요.`

// MomentumAIBot은 "상승 추종 매수" 봇입니다(FR-16).
type MomentumAIBot struct{ Client BedrockClient }

func (MomentumAIBot) Name() string            { return "momentum_ai" }
func (MomentumAIBot) Interval() time.Duration { return aiJudgeInterval }

func (b MomentumAIBot) Decide(ctx context.Context, states map[string]*MarketState) []GlobalDecision {
	decisions, err := b.Client.Invoke(ctx, momentumSystemPrompt, states)
	if err != nil {
		// Bedrock 연결이 안 되는 환경(로컬 dev 등)에서도 이 판단이 없는 것뿐이지
		// 다른 마켓 재생/규칙 기반 봇에는 영향이 없습니다 — 글로벌 봇도 자기만의
		// 고루틴에서 독립적으로 돌기 때문입니다(마켓별 재생 실패 격리와 같은 원칙).
		log.Printf("[momentum_ai] Bedrock 호출 실패, 이번 틱 건너뜀: %v", err)
		return nil
	}
	return decisions
}

// MeanReversionAIBot은 "과열 시 매도" 봇입니다(FR-16).
type MeanReversionAIBot struct{ Client BedrockClient }

func (MeanReversionAIBot) Name() string            { return "mean_reversion_ai" }
func (MeanReversionAIBot) Interval() time.Duration { return aiJudgeInterval }

func (b MeanReversionAIBot) Decide(ctx context.Context, states map[string]*MarketState) []GlobalDecision {
	decisions, err := b.Client.Invoke(ctx, meanReversionSystemPrompt, states)
	if err != nil {
		log.Printf("[mean_reversion_ai] Bedrock 호출 실패, 이번 틱 건너뜀: %v", err)
		return nil
	}
	return decisions
}
