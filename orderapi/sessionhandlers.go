package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"orderapi/backpressure"
	"orderapi/kafkaclient"
	"orderapi/session"
)

// sessionEventsCounter는 세션 클레임/충돌/하트비트/반납을 관찰용으로 셉니다 —
// 특히 claim_conflict(트레이더/리플레이가 동시에 두 개 실행되려는 시도)는
// 로그로만 남았던 걸 그대로 지표로도 남깁니다.
var sessionEventsCounter = prometheus.NewCounterVec(
	prometheus.CounterOpts{
		Name: "session_events_total",
		Help: "Total number of session lifecycle events",
	},
	[]string{"action"},
)

func init() {
	prometheus.MustRegister(sessionEventsCounter)
}

// claimRequest는 POST /v1/sessions의 요청 본문입니다. RunID는 FR-19(리플레이
// 엔진 분산 실행) 지원용 선택 필드 — 같은 리플레이 실행에 속한 여러
// `replayengine` 샤드가 전부 같은 runId를 보내면 한 그룹으로 묶여 다같이
// 세션을 통과합니다(`orderapi/session`의 그룹 모델 참고). 생략하면(트레이더
// 등) 서버가 하나 생성해 멤버 1개짜리 그룹으로 취급 — 예전 동작과 동일합니다.
type claimRequest struct {
	Owner string `json:"owner"`
	RunID string `json:"runId,omitempty"`
	// Speed는 선택입니다(2026-08-20, "실행 결과" 화면에 배속 노출 지원) —
	// trader/replayengine이 자기 -speed 플래그 값을 그대로 보냅니다. 안 보내면
	// 0으로 기록될 뿐 클레임 자체를 막지 않습니다.
	Speed float64 `json:"speed,omitempty"`
	// Date는 선택입니다(2026-08-24, "시스템 종합 현황" 대시보드의 "주문 유실"
	// 지표 지원 — session.RunRecord.Date 참고). replayengine만 의미 있는 값을
	// 보냅니다; trader는 비워 보냅니다.
	Date string `json:"date,omitempty"`
}

// claimResponse는 POST /v1/sessions의 201 응답 본문입니다. ttlSeconds를 실어주는
// 이유는 호출부(trader/replayengine)가 서버가 정한 TTL을 그대로 보고 하트비트
// 주기를 정할 수 있게 하기 위함입니다 — 클라이언트와 서버가 각자 다른 상수를
// 들고 있다가 어긋나는 상황을 막습니다. runId는 요청에서 비워 보냈을 때 서버가
// 실제로 생성한 값을 확인할 수 있도록 항상 채워서 돌려줍니다.
type claimResponse struct {
	SessionID  string `json:"sessionId"`
	RunID      string `json:"runId"`
	Owner      string `json:"owner"`
	ClaimedAt  string `json:"claimedAt"`
	TTLSeconds int    `json:"ttlSeconds"`
}

// claimSessionHandler는 POST /v1/sessions를 처리합니다 — runId 그룹에
// 합류합니다(그룹이 비어있으면 새로 만듦). 이미 다른 runId/owner의 그룹이
// 활성 상태면 409 SESSION_ALREADY_ACTIVE.
func claimSessionHandler(store session.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		reqID := requestID(r)
		w.Header().Set("X-Request-Id", reqID)

		var req claimRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Owner == "" {
			writeError(w, reqID, http.StatusBadRequest, "INVALID_REQUEST", "owner 필드가 필요합니다.")
			return
		}

		info, err := store.Claim(r.Context(), req.Owner, req.RunID, req.Date, req.Speed)
		if err != nil {
			var conflict *session.ConflictError
			if errors.As(err, &conflict) {
				sessionEventsCounter.WithLabelValues("claim_conflict").Inc()
				writeError(w, reqID, http.StatusConflict, "SESSION_ALREADY_ACTIVE",
					"이미 활성 세션이 있습니다 (owner="+conflict.Current.Owner+", claimedAt="+conflict.Current.ClaimedAt.Format(time.RFC3339)+") — 트레이더/시뮬레이터는 동시에 하나만 실행할 수 있습니다.")
				return
			}
			log.Printf("세션 클레임 실패: %v", err)
			writeError(w, reqID, http.StatusInternalServerError, "INTERNAL_ERROR", "세션 클레임에 실패했습니다.")
			return
		}

		sessionEventsCounter.WithLabelValues("claimed").Inc()
		log.Printf("세션 클레임 완료 (sessionId=%s, runId=%s, owner=%s)", info.SessionID, info.RunID, info.Owner)
		writeJSON(w, http.StatusCreated, claimResponse{
			SessionID:  info.SessionID,
			RunID:      info.RunID,
			Owner:      info.Owner,
			ClaimedAt:  info.ClaimedAt.Format(time.RFC3339),
			TTLSeconds: int(info.TTL.Seconds()),
		})
	}
}

// heartbeatResponse는 PUT /v1/sessions/{sessionId}/heartbeat의 응답
// 본문입니다(2026-08-20, 이전에는 빈 본문 204만 줬습니다). StopRequested가
// true면 호출부(trader/replayengine의 RunHeartbeat)가 자기 재생을 스스로
// 정상 종료합니다 — POST /v1/sessions/{runId}/stop 참고.
type heartbeatResponse struct {
	StopRequested bool `json:"stopRequested"`
}

// heartbeatSessionHandler는 PUT /v1/sessions/{sessionId}/heartbeat를 처리합니다.
func heartbeatSessionHandler(store session.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		reqID := requestID(r)
		w.Header().Set("X-Request-Id", reqID)
		sessionID := r.PathValue("sessionId")

		stopRequested, err := store.Heartbeat(r.Context(), sessionID)
		if err != nil {
			if errors.Is(err, session.ErrNotActive) {
				writeError(w, reqID, http.StatusNotFound, "SESSION_NOT_ACTIVE", "해당 세션은 더 이상 활성 상태가 아닙니다 — 만료됐거나 다른 세션이 시작됐을 수 있습니다.")
				return
			}
			log.Printf("세션 하트비트 실패 (sessionId=%s): %v", sessionID, err)
			writeError(w, reqID, http.StatusInternalServerError, "INTERNAL_ERROR", "하트비트 처리에 실패했습니다.")
			return
		}
		sessionEventsCounter.WithLabelValues("heartbeat").Inc()
		writeJSON(w, http.StatusOK, heartbeatResponse{StopRequested: stopRequested})
	}
}

// stopRunHandler는 POST /v1/sessions/{runId}/stop을 처리합니다 — 프론트
// "중지" 버튼용(2026-08-20). sessionId(멤버 단위)가 아니라 runId(그룹 단위)로
// 받습니다 — 프론트는 GET /v1/sessions/last-run에서 runId를 이미 알고
// 있지만, trader/replayengine 프로세스 내부에만 있는 opaque한 sessionId는
// 알 방법이 없기 때문입니다. 실제 정지는 즉시 일어나지 않고 다음 하트비트
// 왕복 때 반영됩니다(session.RequestStop 문서 참고) — 그 사이 재생 중인
// 마켓들은 계속 진행되다가, 신호를 받는 순간 정상 종료 경로(주문 기록
// flush, 세션 반납, 미종결 주문 정리)를 그대로 탑니다.
func stopRunHandler(store session.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		reqID := requestID(r)
		w.Header().Set("X-Request-Id", reqID)
		runID := r.PathValue("runId")

		if err := store.RequestStop(r.Context(), runID); err != nil {
			if errors.Is(err, session.ErrNotActive) {
				writeError(w, reqID, http.StatusNotFound, "NO_ACTIVE_RUN", "현재 진행 중인 실행이 없거나 이미 종료됐습니다.")
				return
			}
			log.Printf("정지 요청 실패 (runId=%s): %v", runID, err)
			writeError(w, reqID, http.StatusInternalServerError, "INTERNAL_ERROR", "정지 요청 처리에 실패했습니다.")
			return
		}
		sessionEventsCounter.WithLabelValues("stop_requested").Inc()
		log.Printf("정지 요청 접수 (runId=%s)", runID)
		w.WriteHeader(http.StatusNoContent)
	}
}

// releaseRequest는 DELETE /v1/sessions/{sessionId}의 선택 요청 본문입니다 —
// 호출부(trader/replayengine)가 이번 실행이 어떻게 끝났는지(완료/실패,
// 메시지)를 함께 보고합니다(2026-08-12, 프론트 "실행 결과" 화면 지원). 본문이
// 없거나 status가 비어있으면 COMPLETED로 간주합니다 — 예전 호출부(본문 없이
// DELETE만 보내는 것)와 호환되게 하려는 것입니다.
type releaseRequest struct {
	Status  string `json:"status,omitempty"`
	Message string `json:"message,omitempty"`
}

// releaseSessionHandler는 DELETE /v1/sessions/{sessionId}를 처리합니다. 이미
// 반납됐거나 존재하지 않는 세션도 404로 명확히 알려줍니다(취소 처리와 달리, 아무
// 상태도 안 남기므로 idempotent-no-op으로 200을 줄 이유가 없습니다).
//
// producer/httpClient/recorderURL/matchingLagChecker는 그룹의 마지막 멤버가
// 반납할 때(=이 실행 전체가 끝날 때)만 도는 미종결 주문 정리(2026-08-19,
// sessioncleanup.go 참고)에 씁니다 — recorderURL이 비어있으면 그 정리 자체를
// 건너뜁니다.
func releaseSessionHandler(store session.Store, producer kafkaclient.Publisher, httpClient *http.Client, recorderURL string, matchingLagChecker backpressure.Checker) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		reqID := requestID(r)
		w.Header().Set("X-Request-Id", reqID)
		sessionID := r.PathValue("sessionId")

		var req releaseRequest
		// 본문이 비어있는 것도 정상입니다(기존 호출부와의 호환) — 파싱 실패는
		// "본문을 아예 안 보냈다"와 "잘못된 JSON을 보냈다"를 구분하지 않고
		// 둘 다 기본값(COMPLETED)으로 취급합니다. 세션 반납 자체를 막을 만큼
		// 중요한 값이 아닙니다.
		json.NewDecoder(r.Body).Decode(&req)
		// 2026-08-26 버그 수정: FAILED만 확인하고 STOPPED을 빠뜨려서, 사용자가
		// "중지" 버튼으로 직접 멈춘 실행(trader/replayengine이 status="STOPPED"로
		// 보고)도 전부 COMPLETED로 저장되고 있었다 — "실행 결과" 화면에서
		// Message는 "사용자 요청으로 정지됨"인데 배지는 "정상 종료"로 뜨는
		// 불일치로 발견됨(Message는 req.Message를 그대로 통과시키니 맞았지만,
		// Status는 이 분기에서 걸러지지 않아 기본값 COMPLETED로 굳어졌다).
		outcome := session.RunOutcome{Status: session.RunStatusCompleted, Message: req.Message}
		switch req.Status {
		case session.RunStatusFailed:
			outcome.Status = session.RunStatusFailed
		case session.RunStatusStopped:
			outcome.Status = session.RunStatusStopped
		}

		record, finalized, err := store.Release(r.Context(), sessionID, outcome)
		if err != nil {
			if errors.Is(err, session.ErrNotActive) {
				writeError(w, reqID, http.StatusNotFound, "SESSION_NOT_ACTIVE", "해당 세션은 이미 해제됐거나 존재하지 않습니다.")
				return
			}
			log.Printf("세션 반납 실패 (sessionId=%s): %v", sessionID, err)
			writeError(w, reqID, http.StatusInternalServerError, "INTERNAL_ERROR", "세션 반납에 실패했습니다.")
			return
		}
		sessionEventsCounter.WithLabelValues("released").Inc()
		log.Printf("세션 반납 완료 (sessionId=%s, status=%s)", sessionID, outcome.Status)
		w.WriteHeader(http.StatusNoContent)

		// 클라이언트(trader/replayengine)에게는 이미 204로 응답을 마쳤습니다 —
		// 정리는 best-effort 백그라운드 작업이라 응답을 기다리게 하지 않습니다.
		// r.Context()는 응답이 끝나면 곧 취소되므로 별도 컨텍스트를 씁니다.
		if finalized {
			go cleanupUnresolvedOrders(context.Background(), httpClient, recorderURL, producer, store, matchingLagChecker, record.RunID, record.StartedAt)
		}
	}
}

// windowTimeFormat — 2026-08-27, "주문 유실"/"매수매도 총량 불일치"가 매 실행마다
// 꼬리에서 몇 건씩 허수로 잡히던 사고 대응. 프론트가 startedAt/endedAt을 그대로
// recorder의 orders/summary·orders/integrity 쿼리 [from, to) 경계로 씁니다(예:
// DashboardView.vue/LoadTestReplayView.vue 둘 다 문자열을 그대로 전달) — 그런데
// time.RFC3339("2006-01-02T15:04:05Z07:00")는 초 단위까지만 표현하는 포맷이라,
// 실행이 끝나는 바로 그 초에 들어온 주문들(실측: 00:25:10.000~00:25:10.322
// 사이 13건)이 잘려나가 "유실"로 잘못 잡혔다. trade_order.submitted_at이
// datetime(3)(밀리초 3자리)이므로 그 정밀도에 맞춘다 — 값이 0이어도(정각) 항상
// ".000"을 붙여 고정폭으로 만든다(RFC3339Nano는 0일 때 소수점을 아예 생략해서
// 프론트/쿼리가 파싱 로직을 둘 다 대응해야 하는 번거로움이 생긴다).
const windowTimeFormat = "2006-01-02T15:04:05.000Z07:00"

// lastRunResponse는 GET /v1/sessions/last-run의 응답 본문입니다 — 프론트의
// 페이퍼 트레이딩 "실행 결과" 화면이 쓰는 실행 상태/시작·종료 시각/오류
// 메시지입니다(주문 접수/체결/미체결 수는 recorder의 별도 엔드포인트가 줌 —
// docs/frontend-backend-integration.md 참고).
type lastRunResponse struct {
	RunID     string  `json:"runId"`
	Owner     string  `json:"owner"`
	Status    string  `json:"status"`
	StartedAt string  `json:"startedAt"`
	EndedAt   string  `json:"endedAt,omitempty"`
	Message   string  `json:"message,omitempty"`
	Speed     float64 `json:"speed,omitempty"`
	// Date는 2026-08-24 추가 — session.RunRecord.Date 참고, replayengine
	// 실행에서만 채워짐.
	Date string `json:"date,omitempty"`
}

func toLastRunResponse(record session.RunRecord) lastRunResponse {
	resp := lastRunResponse{
		RunID:     record.RunID,
		Owner:     record.Owner,
		Status:    record.Status,
		StartedAt: record.StartedAt.Format(windowTimeFormat),
		Message:   record.Message,
		Speed:     record.Speed,
		Date:      record.Date,
	}
	if !record.EndedAt.IsZero() {
		resp.EndedAt = record.EndedAt.Format(windowTimeFormat)
	}
	return resp
}

// lastRunHandler는 GET /v1/sessions/last-run을 처리합니다. 지금까지 한 번도
// 실행된 적이 없으면 404입니다(세션 관련 다른 404들과 같은 관례).
func lastRunHandler(store session.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		reqID := requestID(r)
		w.Header().Set("X-Request-Id", reqID)

		record, found, err := store.LastRun(r.Context())
		if err != nil {
			log.Printf("마지막 실행 조회 실패: %v", err)
			writeError(w, reqID, http.StatusInternalServerError, "INTERNAL_ERROR", "마지막 실행 조회에 실패했습니다.")
			return
		}
		if !found {
			writeError(w, reqID, http.StatusNotFound, "NO_RUN_YET", "아직 실행된 적이 없습니다.")
			return
		}
		writeJSON(w, http.StatusOK, toLastRunResponse(record))
	}
}

// previousRunHandler는 GET /v1/sessions/previous-run을 처리합니다 — 응답
// 모양은 last-run과 동일합니다(2026-08-19, 주문재생 "부하 시나리오 미리보기"
// 화면의 "직전 실행과 비교" 지원). 실행이 2번 미만이었으면 404입니다.
func previousRunHandler(store session.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		reqID := requestID(r)
		w.Header().Set("X-Request-Id", reqID)

		record, found, err := store.PreviousRun(r.Context())
		if err != nil {
			log.Printf("직전 실행 조회 실패: %v", err)
			writeError(w, reqID, http.StatusInternalServerError, "INTERNAL_ERROR", "직전 실행 조회에 실패했습니다.")
			return
		}
		if !found {
			writeError(w, reqID, http.StatusNotFound, "NO_PREVIOUS_RUN", "직전 실행 기록이 없습니다.")
			return
		}
		writeJSON(w, http.StatusOK, toLastRunResponse(record))
	}
}

// runHistoryHandler는 GET /v1/sessions/runs를 처리합니다 — 리플레이 실행만
// 최근 것부터 최대 5건, lastRunResponse와 같은 모양의 배열로 돌려줍니다
// (2026-08-25, "테스트 결과·추적" 화면의 "시뮬레이션 ID로 과거 실행 찾기"
// 지원). 상세 결과(접수/체결/미체결)는 여기서 새로 안 만듭니다 — 프론트가 이
// 목록에서 원하는 항목의 startedAt/endedAt을 가져다 기존
// GET /v1/orders/summary?mode=...&from=...&to=...(recorder)를 그대로
// 호출하면 됩니다. 한 번도 리플레이가 실행된 적이 없어도 빈 배열(에러 아님)을
// 돌려줍니다 — last-run과 달리 "이력이 없다"는 404로 취급할 이유가 없습니다
// (목록 화면은 빈 목록을 그냥 보여주면 되는 것이지, 에러 상태가 아닙니다).
func runHistoryHandler(store session.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		reqID := requestID(r)
		w.Header().Set("X-Request-Id", reqID)

		records, err := store.RunHistory(r.Context())
		if err != nil {
			log.Printf("실행 이력 조회 실패: %v", err)
			writeError(w, reqID, http.StatusInternalServerError, "INTERNAL_ERROR", "실행 이력 조회에 실패했습니다.")
			return
		}
		resp := make([]lastRunResponse, len(records))
		for i, record := range records {
			resp[i] = toLastRunResponse(record)
		}
		writeJSON(w, http.StatusOK, resp)
	}
}

// previousRun2Handler는 GET /v1/sessions/previous-run-2를 처리합니다 —
// previousRunHandler보다 한 번 더 이전 실행(2026-08-25, 프론트 "실행 상태"
// 카드에 최근 3개를 한 줄로 보여달라는 요청 지원). 실행이 3번 미만이었으면
// 404입니다.
func previousRun2Handler(store session.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		reqID := requestID(r)
		w.Header().Set("X-Request-Id", reqID)

		record, found, err := store.PreviousRun2(r.Context())
		if err != nil {
			log.Printf("전전 실행 조회 실패: %v", err)
			writeError(w, reqID, http.StatusInternalServerError, "INTERNAL_ERROR", "전전 실행 조회에 실패했습니다.")
			return
		}
		if !found {
			writeError(w, reqID, http.StatusNotFound, "NO_PREVIOUS_RUN", "전전 실행 기록이 없습니다.")
			return
		}
		writeJSON(w, http.StatusOK, toLastRunResponse(record))
	}
}
