package main

import (
	"errors"
	"log"
	"net/http"
	"time"

	"orderapi/orderrecords"
	"orderapi/validate"
)

// replayPreviewResponse는 GET /v1/jobs/replay-preview의 응답입니다 — 프론트의
// "부하 시나리오 미리보기" 화면(주문 재생 시작 전, 그날 트레이더가 기록해둔
// 주문이 몇 건이고 재생에 대략 얼마나 걸릴지 미리 보여줌)을 지원합니다
// (2026-08-19). MaxEventSpanSeconds는 재생 배속 없이(=1배속) 계산한 값이라,
// 프론트가 재생 시작 폼에서 이미 고른 speed로 나누면 그 배속에서의 예상
// 소요 시간이 됩니다 — speed는 이 API가 몰라도 되는 순수 UI 선택이라
// 쿼리 파라미터로 안 받습니다(값을 바꿀 때마다 재요청할 필요가 없어짐).
type replayPreviewResponse struct {
	Date                string  `json:"date"`
	TotalOrders         int     `json:"totalOrders"`
	MarketsWithRecords  int     `json:"marketsWithRecords"`
	MarketsTotal        int     `json:"marketsTotal"`
	MaxEventSpanSeconds float64 `json:"maxEventSpanSeconds"`
}

// replayPreviewHandler는 GET /v1/jobs/replay-preview?date=YYYY-MM-DD를
// 처리합니다. trader/replayengine과 완전히 같은 날짜 경계 규칙(UTC 캘린더
// 하루, trader/main.go 참고 — 시세 데이터 API의 KST 규칙과는 다름)으로 20개
// 마켓 전부의 기록 파일을 읽어 건수를 세고, 가장 긴 마켓의 (마지막-첫 이벤트)
// 시간 간격을 재생 소요 시간의 추정치로 씁니다 — replayengine이 마켓마다
// 독립된 고루틴으로 동시에 재생하므로, 전체 실행 시간은 이 최댓값에
// 수렴합니다(합이 아님).
//
// 기록이 없는 마켓(ErrNotFound)이나 개별 조회 실패는 건너뛰고 계속 진행합니다
// — collectAllMarkets 등 이 repo 전반의 "한 마켓의 실패가 나머지를 막지
// 않는다" 원칙과 동일합니다.
func replayPreviewHandler(storage orderrecords.Storage) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		reqID := requestID(r)
		w.Header().Set("X-Request-Id", reqID)

		dateStr := r.URL.Query().Get("date")
		if dateStr == "" {
			writeError(w, reqID, http.StatusBadRequest, "MISSING_DATE", "date는 필수입니다 (YYYY-MM-DD).")
			return
		}
		start, err := time.Parse("2006-01-02", dateStr)
		if err != nil {
			writeError(w, reqID, http.StatusBadRequest, "INVALID_DATE", "date 형식이 올바르지 않습니다 (YYYY-MM-DD).")
			return
		}
		start = start.UTC()
		end := start.Add(24 * time.Hour)

		var totalOrders, marketsWithRecords int
		var maxSpanMs int64
		for _, market := range validate.TargetMarkets {
			orders, err := storage.Load(market, start, end)
			if errors.Is(err, orderrecords.ErrNotFound) {
				continue // 그 마켓은 그날 기록된 주문이 없음 — 정상
			}
			if err != nil {
				log.Printf("[%s] 주문 재생 미리보기 조회 실패: %v", market, err)
				continue
			}
			if len(orders) == 0 {
				continue
			}

			totalOrders += len(orders)
			marketsWithRecords++
			// orders는 기록 시점에 이미 ts 오름차순 정렬돼 있습니다
			// (trader/order.InMemoryRecorder.Snapshot) — 첫/마지막 원소가 곧
			// 최소/최대 ts입니다.
			span := orders[len(orders)-1].TS - orders[0].TS
			if span > maxSpanMs {
				maxSpanMs = span
			}
		}

		writeJSON(w, http.StatusOK, replayPreviewResponse{
			Date:                dateStr,
			TotalOrders:         totalOrders,
			MarketsWithRecords:  marketsWithRecords,
			MarketsTotal:        len(validate.TargetMarkets),
			MaxEventSpanSeconds: float64(maxSpanMs) / 1000,
		})
	}
}
