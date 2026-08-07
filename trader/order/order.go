package order

import (
	"context"
	"log"
	"strconv"
	"time"

	"trader/bot"
)

// Order는 Decision을 실제 주문 형태로 바꾼 것입니다.
// 필드 형태는 docs/api-specification.md의 POST /v1/orders 요청 바디에 맞췄습니다
// (price/quantity를 부동소수점 오차 방지를 위해 문자열로 직렬화하는 규칙 포함).
// TS는 주문 접수 API 요청 바디에는 안 쓰이고, 주문 기록(FR-17, record.go)에서
// 시간순 정렬·복원에 씁니다.
type Order struct {
	Market   string
	Side     string
	Price    string
	Quantity string
	TS       int64
}

// NewOrder는 한 마켓의 Decision을 Order로 변환합니다. 가격은 RoundToTick으로 마켓 호가
// 단위의 배수로 스냅합니다 — 그래야 나중에 실제 주문 API에 붙였을 때 INVALID_PRICE_UNIT으로
// 거부되지 않습니다(봇은 스프레드 계산 등으로 임의의 실수 가격을 만들어내므로 이 보정이 필요).
func NewOrder(market string, d bot.Decision) Order {
	price, decimals := RoundToTick(d.Price)
	return Order{
		Market:   market,
		Side:     d.Side,
		Price:    strconv.FormatFloat(price, 'f', decimals, 64),
		Quantity: strconv.FormatFloat(d.Quantity, 'f', -1, 64),
		TS:       time.Now().UnixMilli(),
	}
}

// OrderSubmitter는 생성된 주문을 어딘가로 보냅니다. 주문 접수 API(POST /v1/orders)가
// 준비되면 이 인터페이스를 만족하는 HTTP 구현체로 교체하면 되고, 그 전까지는
// LogOnlySubmitter로 파이프라인 자체를 검증합니다. orderId를 반환하는 이유:
// orderapi가 실제로 발급한 주문 번호를 RecordingSubmitter가 기록에 남겨야
// (FR-17) 나중에 리플레이가 TRADE_ORDER.source_order_id로 원본 주문을 참조할
// 수 있습니다(docs/erd.md 참고) — orderId는 제출이 성공하기 전엔 알 수 없으므로
// Order 자체가 아니라 Submit의 반환값입니다.
type OrderSubmitter interface {
	Submit(ctx context.Context, o Order) (orderID string, err error)
}

// LogOnlySubmitter는 주문 접수 API 연동 전 임시로 쓰는 구현체 — 로그만 남깁니다.
// 실제 API를 안 불러서 orderId가 없으므로 빈 문자열을 반환합니다.
type LogOnlySubmitter struct{}

func (LogOnlySubmitter) Submit(_ context.Context, o Order) (string, error) {
	log.Printf("[order] %s %s qty=%s price=%s (주문 API 미연동 — 로그만 남김)", o.Market, o.Side, o.Quantity, o.Price)
	return "", nil
}
