package bot

import (
	"context"
	"errors"
	"testing"
)

// fakeBedrockClient는 실제 Bedrock 없이 MomentumAIBot/MeanReversionAIBot을
// 검증하기 위한 BedrockClient 구현체입니다.
type fakeBedrockClient struct {
	decisions        []GlobalDecision
	err              error
	lastSystemPrompt string
}

func (f *fakeBedrockClient) Invoke(ctx context.Context, systemPrompt string, states map[string]*MarketState) ([]GlobalDecision, error) {
	f.lastSystemPrompt = systemPrompt
	return f.decisions, f.err
}

func TestMomentumAIBotDecideReturnsClientDecisions(t *testing.T) {
	want := []GlobalDecision{{Market: "KRW-BTC", Decision: Decision{Side: "BUY", Price: 1, Quantity: 1}}}
	client := &fakeBedrockClient{decisions: want}
	b := MomentumAIBot{Client: client}

	got := b.Decide(context.Background(), map[string]*MarketState{})
	if len(got) != 1 || got[0].Market != "KRW-BTC" {
		t.Errorf("Decide() = %+v, want %+v", got, want)
	}
	if client.lastSystemPrompt != momentumSystemPrompt {
		t.Error("MomentumAIBot이 momentumSystemPrompt를 넘기지 않음")
	}
}

func TestMomentumAIBotDecideReturnsNilOnClientError(t *testing.T) {
	client := &fakeBedrockClient{err: errors.New("bedrock 연결 실패")}
	b := MomentumAIBot{Client: client}

	if got := b.Decide(context.Background(), map[string]*MarketState{}); got != nil {
		t.Errorf("Decide() = %v, want nil (에러는 로그만 남기고 판단 없음으로 처리해야 함)", got)
	}
}

func TestMeanReversionAIBotDecideReturnsClientDecisions(t *testing.T) {
	want := []GlobalDecision{{Market: "KRW-ETH", Decision: Decision{Side: "SELL", Price: 1, Quantity: 1}}}
	client := &fakeBedrockClient{decisions: want}
	b := MeanReversionAIBot{Client: client}

	got := b.Decide(context.Background(), map[string]*MarketState{})
	if len(got) != 1 || got[0].Market != "KRW-ETH" {
		t.Errorf("Decide() = %+v, want %+v", got, want)
	}
	if client.lastSystemPrompt != meanReversionSystemPrompt {
		t.Error("MeanReversionAIBot이 meanReversionSystemPrompt를 넘기지 않음")
	}
}

func TestMeanReversionAIBotDecideReturnsNilOnClientError(t *testing.T) {
	client := &fakeBedrockClient{err: errors.New("bedrock 연결 실패")}
	b := MeanReversionAIBot{Client: client}

	if got := b.Decide(context.Background(), map[string]*MarketState{}); got != nil {
		t.Errorf("Decide() = %v, want nil", got)
	}
}
