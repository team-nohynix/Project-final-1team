package bot

import (
	"context"
	"sync"
	"time"
)

// PriceHistorySize는 MarketState가 원형 버퍼로 들고 있는 최근 가격 개수입니다.
// 실측 없이 잡은 기본값 — 나중에 조정 대상입니다.
const PriceHistorySize = 30

// Decision은 봇 하나가 한 판단 주기에 내린 결정입니다.
type Decision struct {
	Side     string // "BUY" 또는 "SELL"
	Price    float64
	Quantity float64
}

// MarketState는 재생 중 계속 갱신되는, 한 마켓의 최근 가격 상태입니다.
// 최신가 하나만이 아니라 원형 버퍼로 최근 N개 가격을 들고 있어서, 마켓메이커처럼
// "지금 가격"만 필요한 봇과 모멘텀/평균회귀처럼 "최근 흐름"이 필요한 봇 둘 다를 지원합니다.
type MarketState struct {
	mu      sync.RWMutex
	history []float64 // 원형 버퍼
	next    int       // 다음에 쓸 위치
	filled  bool      // 버퍼를 한 바퀴 다 채웠는지
}

// NewMarketState는 최근 size개 가격을 보관하는 MarketState를 만듭니다.
func NewMarketState(size int) *MarketState {
	return &MarketState{history: make([]float64, size)}
}

// Update는 가장 최근에 관측된 가격을 원형 버퍼에 추가합니다.
func (s *MarketState) Update(price float64) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.history[s.next] = price
	s.next = (s.next + 1) % len(s.history)
	if s.next == 0 {
		s.filled = true
	}
}

// Price는 가장 최근 가격을 반환합니다. 아직 한 번도 갱신되지 않았으면 ok=false.
func (s *MarketState) Price() (price float64, ok bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if !s.filled && s.next == 0 {
		return 0, false
	}
	last := (s.next - 1 + len(s.history)) % len(s.history)
	return s.history[last], true
}

// History는 보관 중인 가격들을 오래된 것 → 최신 순으로 정렬한 복사본으로 반환합니다.
// 아직 버퍼가 다 안 채워졌으면 그때까지 들어온 만큼만 반환합니다.
func (s *MarketState) History() []float64 {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if !s.filled {
		out := make([]float64, s.next)
		copy(out, s.history[:s.next])
		return out
	}

	out := make([]float64, len(s.history))
	n := copy(out, s.history[s.next:])
	copy(out[n:], s.history[:s.next])
	return out
}

// Bot은 마켓 하나만 보고, 자기 판단 주기(Interval)마다 그 마켓의 MarketState를 참고해
// 주문 여부를 결정합니다. 마켓메이커/노이즈/대량주문자처럼 "마켓별로 독립적으로 존재해야
// 말이 되는" 봇에 씁니다 (여러 마켓을 한 번에 보는 봇은 GlobalBot 참고).
type Bot interface {
	Name() string
	Interval() time.Duration
	Decide(state *MarketState) []Decision
}

// GlobalDecision은 GlobalBot이 특정 마켓에 대해 내린 판단입니다.
type GlobalDecision struct {
	Market string
	Decision
}

// GlobalBot은 여러 마켓의 상태를 한 번에 보고 판단하는 봇입니다 — "20개 종목 중 어디를,
// 얼마나" 판단하는 포트폴리오형 AI 트레이더(모멘텀 추종/평균회귀)에 씁니다. 마켓별로
// 따로 인스턴스를 두지 않고, 판단 주기마다 딱 한 번 호출되어 모든 마켓을 동시에 봅니다.
//
// Bot.Decide와 달리 ctx를 받습니다 — AI 트레이더는 실제로 Bedrock을 호출하는 I/O라
// 타임아웃/취소가 필요하기 때문입니다(마켓별 규칙 기반 봇은 순수 계산이라 필요 없음).
type GlobalBot interface {
	Name() string
	Interval() time.Duration
	Decide(ctx context.Context, states map[string]*MarketState) []GlobalDecision
}
