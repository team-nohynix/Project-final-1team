package main

import (
	"database/sql"
	"encoding/json"
	"log"
	"net/http"
	"time"

	"github.com/redis/go-redis/v9"

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
//
// 2026-08-24: 매 호출마다 q.DashboardMetrics로 직접 라이브 쿼리하던 걸
// pollDashboardMetrics(metrics.go)가 10초마다 이미 채워두는 Redis 캐시
// (dashboardMetricsCacheKey)를 먼저 읽도록 바꿨습니다 — 이 핸들러를 그대로
// 뒀다면 DashboardView가 프론트에서 주기적으로 폴링을 시작하는 순간, 레플리카
// 수와 무관하게 "폴링하는 브라우저 탭 수"만큼 매번 새로 MySQL을 때리게 되고,
// 이건 정확히 2026-08-21 RDS CPU 97~99% 포화 사고(metrics.go 주석 참고)를
// 일으켰던 것과 같은 패턴입니다 — 캐시는 이미 있으니 이 핸들러도 그걸 재사용하면
// 됩니다. 캐시가 비어있을 때만(막 기동해 첫 폴링 주기 전, 또는 캐시 만료
// 직후) 라이브 쿼리로 폴백합니다.
func dashboardMetricsHandler(q query.Querier, redisClient *redis.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if body, err := redisClient.Get(r.Context(), dashboardMetricsCacheKey).Bytes(); err == nil {
			var cached query.DashboardMetrics
			if jsonErr := json.Unmarshal(body, &cached); jsonErr == nil {
				writeJSON(w, http.StatusOK, cached)
				return
			}
			log.Printf("대시보드 지표 캐시 파싱 실패, 라이브 조회로 폴백: %v", err)
		} else if err != redis.Nil {
			log.Printf("대시보드 지표 캐시 조회 실패, 라이브 조회로 폴백: %v", err)
		}

		metrics, err := q.DashboardMetrics(r.Context())
		if err != nil {
			log.Printf("대시보드 지표 조회 실패: %v", err)
			writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "대시보드 지표 조회에 실패했습니다.")
			return
		}
		writeJSON(w, http.StatusOK, metrics)
	}
}

// throughputHandler는 GET /v1/metrics/throughput?from=&to=(둘 다 RFC3339
// 필수)를 처리합니다 — 프론트 DashboardView "주문·체결 처리량" 차트를
// Grafana처럼 임의 기간으로 볼 수 있게 합니다(2026-08-24). dashboardMetricsHandler와
// 달리 **캐시가 없는 라이브 쿼리**입니다 — query.ThroughputSeries 주석의
// throughputMaxRange 이유로 구간 길이는 서버가 거부할 수 있지만, "얼마나
// 자주 불리느냐"는 이 핸들러가 통제할 수 없습니다. 그래서 프론트는 이
// 엔드포인트를 자동 폴링하면 안 되고, 사용자가 기간을 바꿀 때만 불러야
// 합니다(DashboardView.vue 주석 참고) — 2026-08-21 RDS CPU 포화 사고와
// 같은 패턴이 재발하지 않도록 하는 책임이 서버가 아니라 호출 빈도 쪽에
// 있다는 뜻이라, 여기 남겨둡니다.
func throughputHandler(q query.Querier) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		fromStr := r.URL.Query().Get("from")
		toStr := r.URL.Query().Get("to")
		if fromStr == "" || toStr == "" {
			writeError(w, http.StatusBadRequest, "MISSING_RANGE", "from/to는 둘 다 필수입니다 (RFC3339).")
			return
		}
		from, err := time.Parse(time.RFC3339, fromStr)
		if err != nil {
			writeError(w, http.StatusBadRequest, "INVALID_FROM", "from 형식이 올바르지 않습니다 (RFC3339).")
			return
		}
		to, err := time.Parse(time.RFC3339, toStr)
		if err != nil {
			writeError(w, http.StatusBadRequest, "INVALID_TO", "to 형식이 올바르지 않습니다 (RFC3339).")
			return
		}
		if !to.After(from) {
			writeError(w, http.StatusBadRequest, "INVALID_RANGE", "to는 from보다 이후여야 합니다.")
			return
		}
		if to.Sub(from) > query.ThroughputMaxRange {
			writeError(w, http.StatusBadRequest, "RANGE_TOO_WIDE", "구간이 너무 깁니다 (최대 "+query.ThroughputMaxRange.String()+").")
			return
		}

		series, err := q.ThroughputSeries(r.Context(), from, to)
		if err != nil {
			log.Printf("처리량 시계열 조회 실패 (from=%s, to=%s): %v", fromStr, toStr, err)
			writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "처리량 시계열 조회에 실패했습니다.")
			return
		}
		writeJSON(w, http.StatusOK, map[string][]query.MetricsBucket{"series": series})
	}
}

// healthHandler는 GET /v1/health를 처리합니다 — 프론트 DashboardView "시스템
// 구성요소 상태" 패널이 MySQL 연결 자체를 값싸게(집계 쿼리 없이 PingContext만)
// 확인하는 용도입니다. orderapi의 GET /v1/system-status가 이 엔드포인트를
// 내부 호출해 그 결과를 종합합니다.
func healthHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := db.PingContext(r.Context()); err != nil {
			log.Printf("헬스체크 — MySQL 연결 확인 실패: %v", err)
			writeError(w, http.StatusServiceUnavailable, "DB_UNAVAILABLE", "MySQL에 연결할 수 없습니다.")
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
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

// allUnresolvedOrdersHandler는 GET /v1/orders/unresolved/all을 처리합니다 —
// 프론트의 수동 "미종결 주문 일괄 정리" 버튼이 orderapi를 거쳐 부릅니다
// (2026-08-20). unresolvedOrdersHandler(4.9)와 달리 mode/from/to 제한이
// 없습니다 — 이번 세션 하나가 아니라 과거 여러 세션이 누적으로 남긴 백로그
// 전체를 대상으로 합니다.
func allUnresolvedOrdersHandler(q query.Querier) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		orders, err := q.AllUnresolvedOrders(r.Context())
		if err != nil {
			log.Printf("전체 미종결 주문 조회 실패: %v", err)
			writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "전체 미종결 주문 조회에 실패했습니다.")
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
