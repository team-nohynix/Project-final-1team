package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"

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

		info, err := store.Claim(r.Context(), req.Owner, req.RunID)
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

// heartbeatSessionHandler는 PUT /v1/sessions/{sessionId}/heartbeat를 처리합니다.
func heartbeatSessionHandler(store session.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		reqID := requestID(r)
		w.Header().Set("X-Request-Id", reqID)
		sessionID := r.PathValue("sessionId")

		if err := store.Heartbeat(r.Context(), sessionID); err != nil {
			if errors.Is(err, session.ErrNotActive) {
				writeError(w, reqID, http.StatusNotFound, "SESSION_NOT_ACTIVE", "해당 세션은 더 이상 활성 상태가 아닙니다 — 만료됐거나 다른 세션이 시작됐을 수 있습니다.")
				return
			}
			log.Printf("세션 하트비트 실패 (sessionId=%s): %v", sessionID, err)
			writeError(w, reqID, http.StatusInternalServerError, "INTERNAL_ERROR", "하트비트 처리에 실패했습니다.")
			return
		}
		sessionEventsCounter.WithLabelValues("heartbeat").Inc()
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
// producer/httpClient/recorderURL은 그룹의 마지막 멤버가 반납할 때(=이 실행
// 전체가 끝날 때)만 도는 미종결 주문 정리(2026-08-19, sessioncleanup.go
// 참고)에 씁니다 — recorderURL이 비어있으면 그 정리 자체를 건너뜁니다.
func releaseSessionHandler(store session.Store, producer kafkaclient.Publisher, httpClient *http.Client, recorderURL string) http.HandlerFunc {
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
		outcome := session.RunOutcome{Status: session.RunStatusCompleted, Message: req.Message}
		if req.Status == session.RunStatusFailed {
			outcome.Status = session.RunStatusFailed
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
			go cleanupUnresolvedOrders(context.Background(), httpClient, recorderURL, producer, record)
		}
	}
}

// lastRunResponse는 GET /v1/sessions/last-run의 응답 본문입니다 — 프론트의
// 페이퍼 트레이딩 "실행 결과" 화면이 쓰는 실행 상태/시작·종료 시각/오류
// 메시지입니다(주문 접수/체결/미체결 수는 recorder의 별도 엔드포인트가 줌 —
// docs/frontend-backend-integration.md 참고).
type lastRunResponse struct {
	RunID     string `json:"runId"`
	Owner     string `json:"owner"`
	Status    string `json:"status"`
	StartedAt string `json:"startedAt"`
	EndedAt   string `json:"endedAt,omitempty"`
	Message   string `json:"message,omitempty"`
}

func toLastRunResponse(record session.RunRecord) lastRunResponse {
	resp := lastRunResponse{
		RunID:     record.RunID,
		Owner:     record.Owner,
		Status:    record.Status,
		StartedAt: record.StartedAt.Format(time.RFC3339),
		Message:   record.Message,
	}
	if !record.EndedAt.IsZero() {
		resp.EndedAt = record.EndedAt.Format(time.RFC3339)
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
