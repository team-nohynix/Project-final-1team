package main

import (
	"encoding/json"
	"log"
	"net/http"
	"time"

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

// dashboardMetricsHandler는 GET /v1/metrics/dashboard를 처리합니다 — 프론트
// DashboardView의 실시간 지표(docs/frontend-backend-integration.md 3.1: 접수
// TPS/체결 TPS/처리 대기 주문/전체 처리 P99/실행 중인 Pod 수/처리량 라인
// 차트)를 지원합니다. 계산 방식과 근사치의 한계는 query.DashboardMetrics의
// 타입 주석 참고.
func dashboardMetricsHandler(q query.Querier) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		metrics, err := q.DashboardMetrics(r.Context())
		if err != nil {
			log.Printf("대시보드 지표 조회 실패: %v", err)
			writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "대시보드 지표 조회에 실패했습니다.")
			return
		}
		writeJSON(w, http.StatusOK, metrics)
	}
}

// orderSummaryHandler는 GET /v1/orders/summary?mode=...&from=...&to=...를
// 처리합니다 — 프론트의 "페이퍼 트레이딩 실행 결과" 화면(2026-08-12, 주문
// 접수/체결/미체결 수)을 지원합니다. mode/from은 필수, to는 생략하면 지금까지로
// 집계합니다(실행이 아직 진행 중이라 orderapi의 GET /v1/sessions/last-run
// 응답에 endedAt이 없을 때 씀). from/to는 그 응답의 startedAt/endedAt을 그대로
// RFC3339로 넘기면 됩니다 — 세션 가드(orderapi/session)가 트레이더/리플레이
// 엔진을 동시에 하나만 실행되게 막아서, 그 구간엔 다른 실행의 주문이 섞일 수
// 없어 정확한 집계가 됩니다.
func orderSummaryHandler(q query.Querier) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		mode, from, to, ok := parseModeFromTo(w, r)
		if !ok {
			return
		}

		summary, err := q.OrderSummary(r.Context(), mode, from, to)
		if err != nil {
			log.Printf("주문 집계 조회 실패 (mode=%s): %v", mode, err)
			writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "주문 집계 조회에 실패했습니다.")
			return
		}
		writeJSON(w, http.StatusOK, summary)
	}
}

// unresolvedOrdersHandler는 GET /v1/orders/unresolved?mode=...&from=...&to=...를
// 처리합니다 — orderapi가 세션 종료 시점에 그 세션이 남긴 미종결(ACCEPTED/
// PARTIALLY_FILLED) 주문을 취소하기 위해 부릅니다(2026-08-19, 부하테스트
// 반복으로 매칭 엔진 인메모리 오더북에 미체결 주문이 계속 쌓여 OOMKilled까지
// 간 사고 대응). 파라미터 규칙은 orderSummaryHandler(4.7)와 완전히 동일 —
// 세션 가드 덕분에 mode+구간만으로 그 세션의 주문을 정확히 구분할 수 있어
// 별도 세션 식별 컬럼이 필요 없습니다.
func unresolvedOrdersHandler(q query.Querier) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		mode, from, to, ok := parseModeFromTo(w, r)
		if !ok {
			return
		}

		orders, err := q.UnresolvedOrders(r.Context(), mode, from, to)
		if err != nil {
			log.Printf("미종결 주문 조회 실패 (mode=%s): %v", mode, err)
			writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "미종결 주문 조회에 실패했습니다.")
			return
		}
		writeJSON(w, http.StatusOK, map[string][]query.UnresolvedOrder{"orders": orders})
	}
}

// parseModeFromTo는 orderSummaryHandler/unresolvedOrdersHandler가 공유하는
// 쿼리 파라미터 파싱입니다. ok=false면 이미 에러 응답을 썼으니 호출부는 그냥
// return하면 됩니다.
func parseModeFromTo(w http.ResponseWriter, r *http.Request) (mode string, from, to time.Time, ok bool) {
	mode = r.URL.Query().Get("mode")
	if mode != "PAPER_TRADING" && mode != "REPLAY" {
		writeError(w, http.StatusBadRequest, "INVALID_MODE", "mode는 PAPER_TRADING 또는 REPLAY여야 합니다.")
		return "", time.Time{}, time.Time{}, false
	}

	fromStr := r.URL.Query().Get("from")
	if fromStr == "" {
		writeError(w, http.StatusBadRequest, "MISSING_FROM", "from은 필수입니다 (RFC3339).")
		return "", time.Time{}, time.Time{}, false
	}
	var err error
	from, err = time.Parse(time.RFC3339, fromStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_FROM", "from 형식이 올바르지 않습니다 (RFC3339).")
		return "", time.Time{}, time.Time{}, false
	}

	if toStr := r.URL.Query().Get("to"); toStr != "" {
		to, err = time.Parse(time.RFC3339, toStr)
		if err != nil {
			writeError(w, http.StatusBadRequest, "INVALID_TO", "to 형식이 올바르지 않습니다 (RFC3339).")
			return "", time.Time{}, time.Time{}, false
		}
	}

	return mode, from, to, true
}
