// Package order는 접수된 주문 상태를 다룹니다 — 취소 요청 처리(존재 확인, 체결 여부
// 확인)에 필요한 만큼만 인메모리로 추적합니다.
package order

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	"github.com/shopspring/decimal"
)

// 주문 상태 (docs/api-specification.md 5장 order.status).
const (
	StatusAccepted        = "ACCEPTED"
	StatusPartiallyFilled = "PARTIALLY_FILLED"
	StatusFilled          = "FILLED"
	StatusCanceled        = "CANCELED"
)

// Order는 접수된 주문 하나입니다. 필드 이름/모양은 docs/api-specification.md 2장의
// 요청/응답 바디를 그대로 따릅니다 (price/quantity는 부동소수점 오차 방지를 위해 문자열).
type Order struct {
	OrderID          string `json:"orderId"`
	Market           string `json:"market"`
	Side             string `json:"side"`
	Price            string `json:"price"`
	Quantity         string `json:"quantity"`
	Status           string `json:"status"`
	AcceptedAt       string `json:"acceptedAt"`
	CanceledQuantity string `json:"canceledQuantity,omitempty"`
	CanceledAt       string `json:"canceledAt,omitempty"`

	// RemainingQuantity는 ApplyFill이 체결마다 깎아나가는 미체결 잔량입니다
	// (2026-08-10 추가). docs/api-specification.md가 정의한 응답 바디에는 없는
	// 필드라 json:"-"로 숨깁니다 — API 계약을 바꾸는 건 별도로 결정할 일입니다.
	RemainingQuantity string `json:"-"`
}

// Store는 접수된 주문을 인메모리로 추적합니다 — 취소 요청 처리(존재 확인, 체결
// 여부 확인)에 씁니다. ApplyFill(2026-08-10)이 executions 토픽을 구독하는
// kafkaclient.ExecutionConsumer로부터 체결을 받아 ACCEPTED/PARTIALLY_FILLED/
// FILLED 사이 전이를 반영합니다.
//
// **insertedAt/Sweep — 2026-08-27, orderapi가 노드 메모리 부족으로 강제 축출된
// 사고 대응.** orders 맵은 지금까지 한 번 넣은 항목을 절대 지우지 않았습니다 —
// 주석의 "취소 요청 처리에 필요한 만큼만"이라는 원래 의도와 달리 실제로는 이
// 프로세스가 살아있는 동안 접수된 모든 주문이 영원히 쌓였습니다. 오늘처럼 대형
// 리플레이(회당 100만+ 건)를 재시작 없이 여러 번 돌리면 그만큼 누적돼, 실측
// 3GB까지 자라 노드가 "memory pressure"로 이 파드를 축출했습니다(취소 대상은
// 사실상 항상 최근 주문뿐이라 이렇게 오래 들고 있을 이유가 없었음). insertedAt은
// 항목별 저장 시각만 별도로 추적해 Sweep이 오래된 항목을 지울 수 있게 합니다 —
// Order 자체(JSON 응답 모양)는 건드리지 않습니다.
type Store struct {
	mu         sync.Mutex
	orders     map[string]*Order
	insertedAt map[string]time.Time
}

// NewStore는 빈 Store를 만듭니다.
func NewStore() *Store {
	return &Store{orders: make(map[string]*Order), insertedAt: make(map[string]time.Time)}
}

// NewOrderID는 "ord_{YYYYMMDD}_{16자리 hex}" 형태의 주문 번호를 발급합니다.
// 2026-08-21 이전엔 순번 부분이 프로세스 재시작 시 리셋되는 인메모리 카운터였는데,
// 같은 날 orderapi가 재배포/재시작되면(트래픽이 많은 정상적인 하루에도 흔함)
// trade_order에 이미 남아있는 이전 실행의 order_id와 그대로 겹쳐서 INSERT IGNORE에
// 조용히 씹히는 사고(체결이 엉뚱한 옛 주문 행에 merge됨)가 실제로 발생했습니다.
// recorder/idgen.NewExecutionID와 같은 이유로 crypto/rand 8바이트를 씁니다 —
// 재시작/여러 인스턴스에 걸쳐 충돌하지 않아야 하는 영속 ID는 인메모리 순번이
// 아니라 무작위 값이어야 합니다. 날짜 접두사는 로그/DB에서 날짜별로 훑어보기
// 위해 그대로 남겨둡니다.
func (s *Store) NewOrderID(now time.Time) string {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("ord_%s_%016x", now.UTC().Format("20060102"), now.UnixNano())
	}
	return fmt.Sprintf("ord_%s_%s", now.UTC().Format("20060102"), hex.EncodeToString(buf))
}

// Save는 주문을 저장합니다(신규 접수 시). RemainingQuantity를 아직 안 채웠으면
// Quantity로 채웁니다 — 호출부가 RemainingQuantity를 직접 신경 쓸 필요 없게.
func (s *Store) Save(o *Order) {
	if o.RemainingQuantity == "" {
		o.RemainingQuantity = o.Quantity
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.orders[o.OrderID] = o
	s.insertedAt[o.OrderID] = time.Now()
}

// Sweep은 maxAge보다 오래전에 저장된(=Save가 호출된) 주문을 지웁니다 — 이
// Store의 유일한 용도인 취소 요청 처리는 실질적으로 최근 주문에서만 일어나므로,
// maxAge는 넉넉하게 잡아도(예: 1시간) 정상적인 취소/조회를 막지 않으면서 메모리
// 누적을 그 시간만큼으로 상한선을 둡니다. 반환값은 지운 건수입니다(호출부 로깅용).
func (s *Store) Sweep(maxAge time.Duration) int {
	cutoff := time.Now().Add(-maxAge)
	s.mu.Lock()
	defer s.mu.Unlock()
	removed := 0
	for id, t := range s.insertedAt {
		if t.Before(cutoff) {
			delete(s.orders, id)
			delete(s.insertedAt, id)
			removed++
		}
	}
	return removed
}

// Get은 orderID로 주문을 조회합니다. 없으면 ok=false.
func (s *Store) Get(orderID string) (*Order, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	o, ok := s.orders[orderID]
	return o, ok
}

// ApplyFill은 executions 토픽에서 온 체결 하나를 orderID의 잔량에 반영합니다
// (2026-08-10, kafkaclient.ExecutionConsumer가 호출). orderID가 이 Store에
// 없으면(예: orderapi 재시작으로 인메모리 상태가 비워진 뒤 예전 주문의 체결이
// 뒤늦게 도착) found=false를 돌려줄 뿐 에러가 아닙니다 — 이 Store는 RDS(기록기)
// 처럼 정본 저장소가 아니라 취소 처리용 보조 캐시라, 못 찾는 경우를 정상
// 취급합니다(recorder/store.ApplyExecutionsBatch의 "주문 없음" 처리와 같은
// 철학, CLAUDE.md 참고).
//
// 잔량이 이 체결로 0 이하가 되면 FILLED로 확정합니다 — 이미 CANCELED였더라도
// 무조건 덮어씁니다(체결 완료가 그 자체로 "실제로 체결됐다"는 확증이므로,
// 뒤늦게 도착한 오래된 취소 요청이 남긴 상태보다 우선합니다), CanceledAt/
// CanceledQuantity도 같이 지웁니다. recorder/store/mysql.go의 updateFill이
// fill-vs-cancel 도착 순서 문제를 고친 것과 정확히 같은 원칙입니다(orders와
// executions가 서로 독립된 컨슈머라 도착 순서가 실제 발생 순서와 다를 수
// 있음). 잔량이 남으면 ACCEPTED에서만 PARTIALLY_FILLED로 올립니다 — 이미
// CANCELED인 주문은 이 체결이 완결시키지 않는 한 그대로 둡니다(부분체결 후
// 정상적으로 취소된 경우를 뒤늦은 체결이 되돌리면 안 되므로).
func (s *Store) ApplyFill(orderID, quantity string) (found bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	o, ok := s.orders[orderID]
	if !ok {
		return false
	}

	qty, err := decimal.NewFromString(quantity)
	if err != nil {
		return true
	}
	remaining, err := decimal.NewFromString(o.RemainingQuantity)
	if err != nil {
		remaining = decimal.Zero
	}
	remaining = remaining.Sub(qty)

	if remaining.Sign() <= 0 {
		o.RemainingQuantity = "0"
		o.Status = StatusFilled
		o.CanceledAt = ""
		o.CanceledQuantity = ""
	} else {
		o.RemainingQuantity = remaining.String()
		if o.Status == StatusAccepted {
			o.Status = StatusPartiallyFilled
		}
	}
	return true
}
