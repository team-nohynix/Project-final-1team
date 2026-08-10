package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sort"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/document"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
)

// decisionsToolName은 Converse API의 tool-use(함수 호출)로 모델 응답을 강제하는 데
// 쓰는 도구 이름입니다. ToolChoice를 이 이름으로 고정해서, 모델이 자유 텍스트로
// 답하는 대신 항상 이 스키마에 맞는 구조화된 JSON을 반환하게 합니다 — 저렴한
// 모델(Haiku)일수록 형식이 흐트러지기 쉬워서, 파싱을 프롬프트에만 맡기지 않고
// API 차원에서 강제하는 편이 안전합니다.
const decisionsToolName = "submit_trade_decisions"

// decisionsToolInputSchema는 decisionsToolName 도구의 입력 JSON 스키마입니다.
// []GlobalDecision과 1:1로 대응하도록 필드를 맞췄습니다.
var decisionsToolInputSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"decisions": map[string]any{
			"type": "array",
			"items": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"market":   map[string]any{"type": "string", "description": "마켓 코드, 예: KRW-BTC"},
					"side":     map[string]any{"type": "string", "enum": []string{"BUY", "SELL"}},
					"price":    map[string]any{"type": "number"},
					"quantity": map[string]any{"type": "number"},
				},
				"required": []string{"market", "side", "price", "quantity"},
			},
		},
	},
	"required": []string{"decisions"},
}

// marketSnapshot은 모델에게 보내는 마켓 하나의 최근 가격 흐름입니다.
// MarketState.History()가 원래 "모멘텀/평균회귀처럼 최근 흐름이 필요한 봇"을 위해
// 만들어둔 원형 버퍼라, 요약 통계로 압축하지 않고 그 결과를 그대로 실어 보냅니다 —
// 20개 마켓 × PriceHistorySize(30)개면 숫자 600개 정도라 토큰 부담이 크지 않고,
// 미리 요약하면 모델이 판단에 쓸 수 있는 추세 모양/변동성 정보를 먼저 버리게 됩니다.
type marketSnapshot struct {
	Market string    `json:"market"`
	Prices []float64 `json:"prices"`
}

// buildSnapshot은 states로부터 marketSnapshot 목록을 만듭니다. 아직 가격이 한 번도
// 들어오지 않은 마켓(History()가 빈 마켓)은 판단할 근거가 없으므로 건너뜁니다.
// 맵 순회 순서는 원래 정해져 있지 않지만, 마켓 이름으로 정렬해서 반환합니다 —
// 이 프로젝트에서 실행마다 판단이 달라지는 건 어차피 AI 트레이더의 정상 특성이라
// (requirements.md FR-18) 정렬 자체가 정합성에 영향을 주진 않고, 프롬프트/테스트
// 결과를 안정적으로 눈으로 확인하기 위한 것입니다.
func buildSnapshot(states map[string]*MarketState) []marketSnapshot {
	out := make([]marketSnapshot, 0, len(states))
	for market, state := range states {
		prices := state.History()
		if len(prices) == 0 {
			continue
		}
		out = append(out, marketSnapshot{Market: market, Prices: prices})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Market < out[j].Market })
	return out
}

// decisionArgs/decisionArg는 decisionsToolInputSchema에 대응하는, 모델이 실제로
// 채워 보내는 tool-use 입력을 디코딩할 Go 구조체입니다.
type decisionArgs struct {
	Decisions []decisionArg `json:"decisions"`
}

type decisionArg struct {
	Market   string  `json:"market"`
	Side     string  `json:"side"`
	Price    float64 `json:"price"`
	Quantity float64 `json:"quantity"`
}

// parseDecisions는 decisionArgs를 []GlobalDecision으로 변환합니다. 항목 하나가
// 이상해도(가격/수량이 0 이하, side가 BUY/SELL이 아님, 마켓 비어있음) 전체 응답을
// 버리지 않고 그 항목만 건너뜁니다 — 모델 응답 하나에 여러 판단이 섞여 있을 수
// 있고, 일부가 이상하다고 나머지 정상 판단까지 버릴 이유는 없습니다(이 repo
// 전체에 깔린 "한 마켓/한 항목의 실패가 나머지를 막지 않는다" 원칙과 같음).
// 순수 함수라 실제 Bedrock 없이 테스트합니다. skipped는 호출부가 로그를 남기는 데 씁니다.
func parseDecisions(args decisionArgs) (decisions []GlobalDecision, skipped int) {
	for _, d := range args.Decisions {
		if d.Market == "" || (d.Side != "BUY" && d.Side != "SELL") || d.Price <= 0 || d.Quantity <= 0 {
			skipped++
			continue
		}
		decisions = append(decisions, GlobalDecision{
			Market:   d.Market,
			Decision: Decision{Side: d.Side, Price: d.Price, Quantity: d.Quantity},
		})
	}
	return decisions, skipped
}

// BedrockClient는 AI 트레이더 봇이 실제 판단을 요청하는 경로입니다. MomentumAIBot/
// MeanReversionAIBot은 이 인터페이스만 알고, 실제 구현(RealBedrockClient)은 몰라도
// 됩니다 — 테스트에서는 가짜 구현체를 주입할 수 있습니다(kafkaclient.Publisher 등
// 이 repo 전체에 깔린 패턴과 동일).
type BedrockClient interface {
	Invoke(ctx context.Context, systemPrompt string, states map[string]*MarketState) ([]GlobalDecision, error)
}

// RealBedrockClient는 BedrockClient를 AWS Bedrock Converse API로 구현합니다.
// backend/upbit·matching/kafkaclient와 같은 이유로 이 파일은 단위 테스트하지
// 않습니다 — 실제 네트워크/자격증명 왕복이 핵심이라 손으로만(실제 Bedrock 모델
// 액세스가 준비된 뒤) 검증합니다. 순수 로직(buildSnapshot/parseDecisions)만
// 단위 테스트로 분리해둔 이유이기도 합니다.
type RealBedrockClient struct {
	api     *bedrockruntime.Client
	modelID string
}

// NewBedrockClient는 region의 Bedrock 엔드포인트에 modelID로 호출하는 클라이언트를
// 만듭니다. 자격증명은 AWS SDK 기본 체인을 그대로 씁니다(S3 업로드와 같은 방식 —
// 로컬은 환경변수/공유 프로파일, 배포 환경은 EC2 인스턴스 프로파일/EKS IRSA가
// 자동으로 채워줌). modelID의 정확한 값은 이 코드에서 하드코딩하지 않고 호출부가
// 넘겨줍니다 — Bedrock에서 실제로 활성화된 모델 ID를 확인해야 하는 값이라
// config.go의 필수 환경변수(BEDROCK_MODEL_ID)로 받습니다.
func NewBedrockClient(ctx context.Context, region, modelID string) (*RealBedrockClient, error) {
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(region))
	if err != nil {
		return nil, fmt.Errorf("AWS 설정 로드 실패: %w", err)
	}
	return &RealBedrockClient{api: bedrockruntime.NewFromConfig(cfg), modelID: modelID}, nil
}

// Invoke는 systemPrompt(봇의 전략 설명)와 states의 현재 가격 흐름을 Bedrock에
// 보내고, tool-use로 강제한 구조화된 응답을 []GlobalDecision으로 돌려줍니다.
// 판단할 가격 데이터가 있는 마켓이 하나도 없으면(재생 시작 직후 등) 호출 자체를
// 건너뜁니다 — 빈 프롬프트로 모델을 부를 이유가 없습니다.
func (c *RealBedrockClient) Invoke(ctx context.Context, systemPrompt string, states map[string]*MarketState) ([]GlobalDecision, error) {
	snapshot := buildSnapshot(states)
	if len(snapshot) == 0 {
		return nil, nil
	}

	userJSON, err := json.Marshal(snapshot)
	if err != nil {
		return nil, fmt.Errorf("마켓 스냅샷 직렬화 실패: %w", err)
	}

	resp, err := c.api.Converse(ctx, &bedrockruntime.ConverseInput{
		ModelId: aws.String(c.modelID),
		System:  []types.SystemContentBlock{&types.SystemContentBlockMemberText{Value: systemPrompt}},
		Messages: []types.Message{
			{
				Role:    types.ConversationRoleUser,
				Content: []types.ContentBlock{&types.ContentBlockMemberText{Value: string(userJSON)}},
			},
		},
		ToolConfig: &types.ToolConfiguration{
			Tools: []types.Tool{
				&types.ToolMemberToolSpec{Value: types.ToolSpecification{
					Name:        aws.String(decisionsToolName),
					Description: aws.String("판단한 매수/매도 결정을 제출합니다. 판단할 게 없으면 decisions를 빈 배열로 제출합니다."),
					InputSchema: &types.ToolInputSchemaMemberJson{Value: document.NewLazyDocument(decisionsToolInputSchema)},
				}},
			},
			ToolChoice: &types.ToolChoiceMemberTool{Value: types.SpecificToolChoice{Name: aws.String(decisionsToolName)}},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("Bedrock Converse 호출 실패: %w", err)
	}

	msg, ok := resp.Output.(*types.ConverseOutputMemberMessage)
	if !ok {
		return nil, fmt.Errorf("응답에 메시지가 없음 (stopReason=%v)", resp.StopReason)
	}

	for _, block := range msg.Value.Content {
		toolUse, ok := block.(*types.ContentBlockMemberToolUse)
		if !ok {
			continue
		}
		var args decisionArgs
		if err := toolUse.Value.Input.UnmarshalSmithyDocument(&args); err != nil {
			return nil, fmt.Errorf("tool-use 입력 디코딩 실패: %w", err)
		}
		decisions, skipped := parseDecisions(args)
		if skipped > 0 {
			log.Printf("Bedrock 응답 중 %d건은 형식이 이상해 건너뜀", skipped)
		}
		return decisions, nil
	}

	return nil, fmt.Errorf("응답에 tool-use 블록이 없음 (stopReason=%v)", resp.StopReason)
}
