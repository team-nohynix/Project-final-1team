// Package events는 orders/executions 토픽의 와이어 메시지 모양을 기록기 관점에서
// 다룹니다. orderapi/kafkaclient.go의 orderEvent, matching/kafkaclient/producer.go의
// executionMessage와 독립적으로 다시 선언합니다(모듈 간 타입 비공유 원칙) — 필드
// 모양이 바뀌면 양쪽을 손으로 맞춰야 합니다.
package events

// orderEvent/executionMessage는 실제 Kafka 메시지의 JSON 모양입니다. 모르는
// 필드는 encoding/json이 그냥 무시하므로, orderapi/matching 쪽에 새 필드가
// 생겨도 이 디코더가 깨지지는 않습니다.
type orderEvent struct {
	Type            string `json:"type"`
	OrderID         string `json:"orderId"`
	Market          string `json:"market"`
	Side            string `json:"side,omitempty"`
	Price           string `json:"price,omitempty"`
	Quantity        string `json:"quantity,omitempty"`
	AcceptedAt      string `json:"acceptedAt,omitempty"`
	ClientRequestID string `json:"clientRequestId,omitempty"`
	Mode            string `json:"mode,omitempty"`
	CanceledAt      string `json:"canceledAt,omitempty"`
	SourceOrderID   string `json:"sourceOrderId,omitempty"`
}

type executionMessage struct {
	Market      string `json:"market"`
	BuyOrderID  string `json:"buyOrderId"`
	SellOrderID string `json:"sellOrderId"`
	Price       string `json:"price"`
	Quantity    string `json:"quantity"`
}

// assignmentMessage는 matching/kafkaclient/assignment_producer.go의 메시지 모양과
// 독립적으로 다시 선언한 것입니다(모듈 간 타입 비공유 원칙).
type assignmentMessage struct {
	Type             string `json:"type"` // "ASSIGNED" | "RELEASED"
	Market           string `json:"market"`
	EngineInstanceID string `json:"engineInstanceId"`
	At               string `json:"at"`
}

// EventType은 orders 토픽 메시지의 종류입니다.
type EventType string

const (
	OrderNew    EventType = "NEW"
	OrderCancel EventType = "CANCEL"
)

// OrderEvent는 orders 토픽 메시지 하나를 기록기가 다루기 좋은 형태로 디코딩한
// 것입니다. CANCEL 이벤트는 Side/Price/Quantity/AcceptedAt/ClientRequestID/Mode/
// SourceOrderID가 비어 있습니다(원본 메시지에도 없음). NEW 이벤트도
// SourceOrderID는 trader의 신규 주문이면 빈 문자열입니다(리플레이 주문만 값이 있음).
type OrderEvent struct {
	Type            EventType
	OrderID         string
	Market          string
	Side            string
	Price           string
	Quantity        string
	AcceptedAt      string
	ClientRequestID string
	Mode            string
	CanceledAt      string
	SourceOrderID   string
}

// ExecutionEvent는 executions 토픽 메시지 하나를 디코딩한 것입니다.
type ExecutionEvent struct {
	Market      string
	BuyOrderID  string
	SellOrderID string
	Price       string
	Quantity    string
}

// AssignmentType은 assignments 토픽 메시지의 종류입니다.
type AssignmentType string

const (
	AssignmentAssigned AssignmentType = "ASSIGNED"
	AssignmentReleased AssignmentType = "RELEASED"
)

// AssignmentEvent는 assignments 토픽 메시지 하나를 디코딩한 것입니다(FR-11).
type AssignmentEvent struct {
	Type             AssignmentType
	Market           string
	EngineInstanceID string
	At               string
}
