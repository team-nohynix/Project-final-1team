package main

import (
	"encoding/json"
	"log"
	"net/http"

	"orderapi/jobtrigger"
)

// startJobHandler는 POST /v1/jobs를 처리합니다 — 프론트엔드의 "시작" 버튼이
// 호출할 엔드포인트입니다. 실제 K8s Job 생성은 여기서 하지 않고 SQS에 발행만
// 합니다 — job-trigger Lambda가 소비해서 만듭니다(infra/job-trigger.tf,
// infra/lambda/job-trigger/index.py 참고). orderapi가 K8s API 권한을 아예
// 갖지 않아도 되게 하려는 설계입니다(공격 표면 축소).
//
// 202 Accepted는 "요청이 큐에 들어갔다"는 뜻일 뿐 "Job이 실제로 떴다"는
// 보장이 아닙니다 — Lambda가 실패하거나 trader/replayengine이 세션 클레임에
// 실패(이미 다른 실행이 진행 중)할 수도 있습니다. 진행 상황 확인은 이
// 엔드포인트가 아니라 K8s 자체나 별도 세션 상태 조회로 해야 합니다(CLAUDE.md의
// "읽기 전용 세션 상태 엔드포인트" 제안 — 아직 미구현).
func startJobHandler(publisher jobtrigger.Publisher) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		reqID := requestID(r)
		w.Header().Set("X-Request-Id", reqID)

		var req jobtrigger.Request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, reqID, http.StatusBadRequest, "INVALID_REQUEST", "요청 본문 JSON 파싱 실패")
			return
		}

		if code, msg, ok := jobtrigger.ValidateRequest(req); !ok {
			writeError(w, reqID, http.StatusBadRequest, code, msg)
			return
		}

		if err := publisher.Publish(r.Context(), req); err != nil {
			log.Printf("작업 트리거 발행 실패 (jobType=%s, date=%s): %v", req.JobType, req.Date, err)
			writeError(w, reqID, http.StatusInternalServerError, "INTERNAL_ERROR", "작업 트리거 발행에 실패했습니다.")
			return
		}
		log.Printf("작업 트리거 발행 완료 (jobType=%s, date=%s)", req.JobType, req.Date)

		writeJSON(w, http.StatusAccepted, map[string]string{"status": "queued"})
	}
}
