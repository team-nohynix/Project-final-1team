package events

import (
	"encoding/json"
	"fmt"
)

// DecodeOrderEvent는 orders 토픽 메시지 하나를 파싱합니다. Type이 NEW/CANCEL이
// 아니면 에러를 반환합니다(orderapi/matching이 오늘 쓰는 두 값 외엔 알려진 게 없음).
func DecodeOrderEvent(data []byte) (OrderEvent, error) {
	var raw orderEvent
	if err := json.Unmarshal(data, &raw); err != nil {
		return OrderEvent{}, fmt.Errorf("orders 메시지 파싱 실패: %w", err)
	}

	switch raw.Type {
	case "NEW", "CANCEL":
	default:
		return OrderEvent{}, fmt.Errorf("알 수 없는 이벤트 타입: %q", raw.Type)
	}

	return OrderEvent{
		Type:            EventType(raw.Type),
		OrderID:         raw.OrderID,
		Market:          raw.Market,
		Side:            raw.Side,
		Price:           raw.Price,
		Quantity:        raw.Quantity,
		AcceptedAt:      raw.AcceptedAt,
		ClientRequestID: raw.ClientRequestID,
		Mode:            raw.Mode,
		CanceledAt:      raw.CanceledAt,
		SourceOrderID:   raw.SourceOrderID,
	}, nil
}

// DecodeExecution은 executions 토픽 메시지 하나를 파싱합니다.
func DecodeExecution(data []byte) (ExecutionEvent, error) {
	var raw executionMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return ExecutionEvent{}, fmt.Errorf("executions 메시지 파싱 실패: %w", err)
	}
	return ExecutionEvent{
		Market:      raw.Market,
		BuyOrderID:  raw.BuyOrderID,
		SellOrderID: raw.SellOrderID,
		Price:       raw.Price,
		Quantity:    raw.Quantity,
	}, nil
}

// DecodeAssignment은 assignments 토픽 메시지 하나를 파싱합니다.
func DecodeAssignment(data []byte) (AssignmentEvent, error) {
	var raw assignmentMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return AssignmentEvent{}, fmt.Errorf("assignments 메시지 파싱 실패: %w", err)
	}

	switch raw.Type {
	case "ASSIGNED", "RELEASED":
	default:
		return AssignmentEvent{}, fmt.Errorf("알 수 없는 이벤트 타입: %q", raw.Type)
	}

	return AssignmentEvent{
		Type:             AssignmentType(raw.Type),
		Market:           raw.Market,
		EngineInstanceID: raw.EngineInstanceID,
		At:               raw.At,
	}, nil
}
