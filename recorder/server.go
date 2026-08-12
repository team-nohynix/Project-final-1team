package main

import (
	"encoding/json"
	"log"
	"net/http"

	"recorder/query"
)

// errorResponse는 orderapi/backend의 공통 오류 응답 포맷과 같은 모양입니다
// (모듈 간 타입 비공유 원칙에 따라 독립적으로 다시 선언).
type errorResponse struct {
	ErrorCode string `json:"errorCode"`
	Message   string `json:"message"`
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, errorCode, message string) {
	writeJSON(w, status, errorResponse{ErrorCode: errorCode, Message: message})
}

// traceHandler는 GET /v1/trace/{orderId}를 처리합니다 — 프론트
// TestResultTrackingView의 트레이스 검색을 지원합니다(docs/
// frontend-backend-integration.md 3.2 참고). query.Querier가 반환하는
// OrderTrace의 시각 필드는 recorder가 실제로 갖고 있는 것(접수/체결/취소)뿐이라,
// 프론트가 기대하는 "5단계 파이프라인" 형태와는 다릅니다 — 프론트 쪽에서 이
// 응답 모양에 맞게 화면을 다시 설계해야 합니다.
func traceHandler(q query.Querier) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		orderID := r.PathValue("orderId")
		trace, found, err := q.TraceOrder(r.Context(), orderID)
		if err != nil {
			log.Printf("트레이스 조회 실패 (orderId=%s): %v", orderID, err)
			writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "트레이스 조회에 실패했습니다.")
			return
		}
		if !found {
			writeError(w, http.StatusNotFound, "ORDER_NOT_FOUND", "해당 주문을 찾을 수 없습니다.")
			return
		}
		writeJSON(w, http.StatusOK, trace)
	}
}

// enginesHandler는 GET /v1/matching/engines를 처리합니다 — 프론트
// MatchingEngineView의 엔진 목록을 지원합니다. 지금 담당 중인(released_at IS
// NULL) 배정만 모아 엔진별로 묶어서 돌려줍니다.
func enginesHandler(q query.Querier) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		engines, err := q.ListEngines(r.Context())
		if err != nil {
			log.Printf("엔진 목록 조회 실패: %v", err)
			writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "엔진 목록 조회에 실패했습니다.")
			return
		}
		writeJSON(w, http.StatusOK, map[string][]query.EngineAssignment{"engines": engines})
	}
}
