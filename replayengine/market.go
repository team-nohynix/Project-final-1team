package main

// TargetMarkets는 업비트 원화 마켓 20개 목록입니다(requirements.md 1.1.4).
// backend/trader/orderapi/matching의 목록과 값은 같지만, 모듈 독립성 원칙에 따라
// 여기서도 따로 선언합니다.
var TargetMarkets = []string{
	"KRW-USDT", "KRW-BTC", "KRW-XRP", "KRW-ETH", "KRW-ONDO",
	"KRW-LA", "KRW-SHIB", "KRW-RE", "KRW-DOGE", "KRW-SLX",
	"KRW-KAITO", "KRW-SOL", "KRW-XLM", "KRW-WLD", "KRW-MIRA",
	"KRW-ERA", "KRW-ADA", "KRW-AI", "KRW-NEAR", "KRW-ARX",
}
