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
// defaultOrderBucket이 비어있지 않으면, 요청이 orderBucket을 안 보냈을 때
// 이 값으로 채웁니다 — 프론트가 버킷 이름을 몰라도(알 필요도 없어야 함)
// trader/replayengine이 항상 같은 곳(ORDER_RECORDS_BUCKET, GET
// /v1/jobs/replay-preview가 읽는 곳과 동일)에 기록하게 하려는 것입니다.
// 2026-08-20: 이 기본값이 없어서 실제로 -order-bucket이 한 번도 안 실려
// trader가 로컬 ./orders로 폴백했고, 그 로컬 쓰기마저 컨테이너 권한 문제로
// 실패해 주문 기록이 통째로 유실되고 있었다(라이브 Job 실행 인자로 확인).
func startJobHandler(publisher jobtrigger.Publisher, defaultOrderBucket string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		reqID := requestID(r)
		w.Header().Set("X-Request-Id", reqID)

		var req jobtrigger.Request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, reqID, http.StatusBadRequest, "INVALID_REQUEST", "요청 본문 JSON 파싱 실패")
			return
		}
		if req.OrderBucket == "" {
			req.OrderBucket = defaultOrderBucket
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
