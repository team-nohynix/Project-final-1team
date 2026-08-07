package main

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"time"

	"orderapi/session"
)

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
				writeError(w, reqID, http.StatusConflict, "SESSION_ALREADY_ACTIVE",
					"이미 활성 세션이 있습니다 (owner="+conflict.Current.Owner+", claimedAt="+conflict.Current.ClaimedAt.Format(time.RFC3339)+") — 트레이더/시뮬레이터는 동시에 하나만 실행할 수 있습니다.")
				return
			}
			log.Printf("세션 클레임 실패: %v", err)
			writeError(w, reqID, http.StatusInternalServerError, "INTERNAL_ERROR", "세션 클레임에 실패했습니다.")
			return
		}

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
		w.WriteHeader(http.StatusNoContent)
	}
}

// releaseSessionHandler는 DELETE /v1/sessions/{sessionId}를 처리합니다. 이미
// 반납됐거나 존재하지 않는 세션도 404로 명확히 알려줍니다(취소 처리와 달리, 아무
// 상태도 안 남기므로 idempotent-no-op으로 200을 줄 이유가 없습니다).
func releaseSessionHandler(store session.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		reqID := requestID(r)
		w.Header().Set("X-Request-Id", reqID)
		sessionID := r.PathValue("sessionId")

		if err := store.Release(r.Context(), sessionID); err != nil {
			if errors.Is(err, session.ErrNotActive) {
				writeError(w, reqID, http.StatusNotFound, "SESSION_NOT_ACTIVE", "해당 세션은 이미 해제됐거나 존재하지 않습니다.")
				return
			}
			log.Printf("세션 반납 실패 (sessionId=%s): %v", sessionID, err)
			writeError(w, reqID, http.StatusInternalServerError, "INTERNAL_ERROR", "세션 반납에 실패했습니다.")
			return
		}
		log.Printf("세션 반납 완료 (sessionId=%s)", sessionID)
		w.WriteHeader(http.StatusNoContent)
	}
}
