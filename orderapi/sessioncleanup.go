package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"time"

	"orderapi/kafkaclient"
	"orderapi/session"
)

// unresolvedOrder는 recorder의 GET /v1/orders/unresolved 응답 항목입니다 —
// recorder/query.UnresolvedOrder와 같은 모양(모듈 간 타입 비공유 원칙에 따라
// 독립적으로 다시 선언).
type unresolvedOrder struct {
	OrderID string `json:"orderId"`
	Market  string `json:"market"`
}

type unresolvedOrdersResponse struct {
	Orders []unresolvedOrder `json:"orders"`
}

// modeForOwner는 session.RunRecord.Owner("trader"/"replayengine")를
// recorder/orderapi가 쓰는 mode("PAPER_TRADING"/"REPLAY")로 바꿉니다.
// 이 대응은 trader가 항상 X-Order-Mode: PAPER_TRADING을, replayengine이
// 항상 REPLAY를 보낸다는 기존 규칙(CLAUDE.md의 "Mode 배관" 참고)을 그대로
// 따릅니다 — 새 규칙을 만드는 게 아니라 이미 있는 대응을 재사용하는 것.
func modeForOwner(owner string) (string, bool) {
	switch owner {
	case "trader":
		return "PAPER_TRADING", true
	case "replayengine":
		return "REPLAY", true
	default:
		return "", false
	}
}

// cleanupUnresolvedOrders는 세션 하나가 완전히 끝났을 때(그룹의 마지막
// 멤버가 반납했을 때) 그 세션이 남긴 미종결 주문을 recorder에 물어보고,
// 각각 취소 이벤트를 발행합니다(2026-08-19) — 부하테스트를 반복하면 매칭
// 엔진의 인메모리 오더북에 체결 안 된 주문이 계속 쌓여 결국 OOMKilled까지
// 갔던 사고 대응입니다. "취소"를 쓰는 이유는 이 시스템에서 이미 "오더북에서
// 빼기"와 동의어이기 때문 — 새 메커니즘(예: 오더북 강제 리셋)을 만들 필요가
// 없고, recorder에도 정상적으로 CANCELED/canceled_at이 기록되어 나중에
// 조회해도 데이터가 정직합니다(단순히 메모리에서만 지우면 RDS엔 영원히
// "미체결"로 남아 통계가 왜곡됩니다).
//
// 실패해도 세션 반납 자체(이미 클라이언트에 204로 응답 끝남)에는 영향을
// 주지 않습니다 — best-effort 백그라운드 정리이지, 세션 종료 자체를 막을
// 이유가 없는 부가 기능입니다. recorderURL이 비어있으면(config.go 참고)
// 아예 시도하지 않습니다.
func cleanupUnresolvedOrders(ctx context.Context, httpClient *http.Client, recorderURL string, producer kafkaclient.Publisher, record session.RunRecord) {
	if recorderURL == "" {
		return
	}

	mode, ok := modeForOwner(record.Owner)
	if !ok {
		log.Printf("세션 정리 건너뜀 — 알 수 없는 owner %q (runId=%s)", record.Owner, record.RunID)
		return
	}
	if record.StartedAt.IsZero() {
		log.Printf("세션 정리 건너뜀 — startedAt 없음 (runId=%s)", record.RunID)
		return
	}
	endedAt := record.EndedAt
	if endedAt.IsZero() {
		endedAt = time.Now().UTC()
	}

	orders, err := fetchUnresolvedOrders(ctx, httpClient, recorderURL, mode, record.StartedAt, endedAt)
	if err != nil {
		log.Printf("세션 정리 실패 — 미종결 주문 조회 실패 (runId=%s): %v", record.RunID, err)
		return
	}
	if len(orders) == 0 {
		log.Printf("세션 정리 완료 — 미종결 주문 없음 (runId=%s)", record.RunID)
		return
	}

	canceledAt := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	canceled := 0
	for _, o := range orders {
		if err := producer.PublishCancel(ctx, o.OrderID, o.Market, canceledAt); err != nil {
			log.Printf("세션 정리 중 취소 발행 실패 (runId=%s, orderId=%s): %v", record.RunID, o.OrderID, err)
			continue
		}
		canceled++
	}
	log.Printf("세션 정리 완료 — 미종결 주문 %d/%d건 취소 발행 (runId=%s, mode=%s)", canceled, len(orders), record.RunID, mode)
}

// cleanupAllUnresolvedOrdersHandler는 POST /v1/admin/cleanup-unresolved-orders를
// 처리합니다 — 프론트의 수동 "미종결 주문 일괄 정리" 버튼(2026-08-20)이
// 부릅니다. cleanupUnresolvedOrders(세션 종료 시 자동, 그 세션 몫만)와 달리
// mode/기간 제한 없이 지금 시점에 남아있는 미종결 주문 전부를 대상으로 하고,
// 프론트가 결과 건수를 바로 보여줘야 해서 백그라운드 고루틴이 아니라
// 동기적으로 처리하고 취소 건수를 응답합니다. 오래 걸릴 수 있어(수만 건이면
// 개별 Kafka publish가 누적됨) 호출부 HTTP 클라이언트는 넉넉한 타임아웃을
// 잡아야 합니다.
func cleanupAllUnresolvedOrdersHandler(httpClient *http.Client, recorderURL string, producer kafkaclient.Publisher) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		reqID := requestID(r)
		w.Header().Set("X-Request-Id", reqID)

		if recorderURL == "" {
			writeError(w, reqID, http.StatusServiceUnavailable, "RECORDER_URL_NOT_CONFIGURED", "RECORDER_URL이 설정되어 있지 않습니다.")
			return
		}

		u := recorderURL + "/v1/orders/unresolved/all"
		req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, u, nil)
		if err != nil {
			writeError(w, reqID, http.StatusInternalServerError, "INTERNAL_ERROR", "요청 생성에 실패했습니다.")
			return
		}
		resp, err := httpClient.Do(req)
		if err != nil {
			log.Printf("전체 미종결 주문 조회 실패: %v", err)
			writeError(w, reqID, http.StatusBadGateway, "RECORDER_UNAVAILABLE", "recorder 조회에 실패했습니다.")
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			writeError(w, reqID, http.StatusBadGateway, "RECORDER_UNAVAILABLE", fmt.Sprintf("recorder가 %d를 반환함", resp.StatusCode))
			return
		}
		var body unresolvedOrdersResponse
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			writeError(w, reqID, http.StatusBadGateway, "RECORDER_UNAVAILABLE", "recorder 응답 파싱에 실패했습니다.")
			return
		}

		canceledAt := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
		canceled := 0
		for _, o := range body.Orders {
			if err := producer.PublishCancel(r.Context(), o.OrderID, o.Market, canceledAt); err != nil {
				log.Printf("일괄 정리 중 취소 발행 실패 (orderId=%s): %v", o.OrderID, err)
				continue
			}
			canceled++
		}
		log.Printf("미종결 주문 일괄 정리 완료 — %d/%d건 취소 발행", canceled, len(body.Orders))
		writeJSON(w, http.StatusOK, map[string]int{"canceled": canceled, "total": len(body.Orders)})
	}
}

func fetchUnresolvedOrders(ctx context.Context, httpClient *http.Client, recorderURL, mode string, from, to time.Time) ([]unresolvedOrder, error) {
	u := fmt.Sprintf("%s/v1/orders/unresolved?mode=%s&from=%s&to=%s",
		recorderURL,
		url.QueryEscape(mode),
		url.QueryEscape(from.Format(time.RFC3339)),
		url.QueryEscape(to.Format(time.RFC3339)),
	)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("recorder가 %d를 반환함", resp.StatusCode)
	}
	var body unresolvedOrdersResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("응답 파싱 실패: %w", err)
	}
	return body.Orders, nil
}
