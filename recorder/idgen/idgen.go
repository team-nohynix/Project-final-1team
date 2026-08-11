// Package idgen은 기록기가 스스로 발급해야 하는 ID를 만듭니다 — Kafka
// executions 메시지에는 execution_id가 없어서, 기록기가 직접 만들어야 합니다.
package idgen

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"
)

// NewExecutionID는 "exec_" + 16바이트 랜덤 hex 형태의 execution_id를 만듭니다.
// orderapi/session.newSessionID와 같은 모양입니다 — 영속 저장되는 비즈니스 ID라
// (요청 추적용 짧은 ID가 아니라) 프로세스 재시작/여러 인스턴스에 걸쳐 충돌하지
// 않아야 하므로, 인메모리 순번 카운터(order.NewOrderID 방식)가 아니라
// crypto/rand 기반 무작위 값을 씁니다.
func NewExecutionID() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("exec_%d", time.Now().UnixNano())
	}
	return "exec_" + hex.EncodeToString(buf)
}

// NewAssignmentID는 같은 이유로 matching_engine_assignment의 assignment_id를 만듭니다.
func NewAssignmentID() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("assign_%d", time.Now().UnixNano())
	}
	return "assign_" + hex.EncodeToString(buf)
}
