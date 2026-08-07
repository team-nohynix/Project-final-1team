// Package order는 접수된 주문 상태를 다룹니다 — 취소 요청 처리(존재 확인, 체결 여부
// 확인)에 필요한 만큼만 인메모리로 추적합니다.
package order

import (
	"fmt"
	"sync"
	"time"
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
}

// Store는 접수된 주문을 인메모리로 추적합니다 — 취소 요청 처리(존재 확인, 체결
// 여부 확인)에 씁니다. 매칭 엔진(B)이 executions를 통해 체결 상태를 알려오는 경로가
// 아직 orderapi 쪽에 연결돼 있지 않아서, 지금은 ACCEPTED에서 CANCELED로만 바뀔 수
// 있고 PARTIALLY_FILLED/FILLED로의 전이는 그 경로가 생기면 반영할 자리입니다.
type Store struct {
	mu      sync.Mutex
	orders  map[string]*Order
	nextSeq int
}

// NewStore는 빈 Store를 만듭니다.
func NewStore() *Store {
	return &Store{orders: make(map[string]*Order)}
}

// NewOrderID는 "ord_{YYYYMMDD}_{7자리 순번}" 형태의 주문 번호를 발급합니다
// (docs/api-specification.md 2.1 예시와 동일한 모양). 순번은 프로세스 재시작 시
// 리셋되는 인메모리 카운터입니다 — 영속 저장소가 아직 없어서 생기는 한계입니다.
func (s *Store) NewOrderID(now time.Time) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextSeq++
	return fmt.Sprintf("ord_%s_%07d", now.UTC().Format("20060102"), s.nextSeq)
}

// Save는 주문을 저장합니다(신규 접수 시).
func (s *Store) Save(o *Order) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.orders[o.OrderID] = o
}

// Get은 orderID로 주문을 조회합니다. 없으면 ok=false.
func (s *Store) Get(orderID string) (*Order, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	o, ok := s.orders[orderID]
	return o, ok
}
