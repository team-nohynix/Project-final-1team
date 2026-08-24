package main

import (
	"net/http"
	"sync"
	"time"
)

// 드롭된(429 백프레셔로 거절된) 주문 시계열 (2026-08-24, 사용자 요청) —
// "주문·체결 처리량" 차트에 접수/체결과 나란히 보여주기 위한 것입니다.
//
// **정의**: 여기서 세는 건 429(rejected_backpressure) *응답 횟수*입니다 —
// 어떤 주문이 재시도 끝에 결국 접수됐어도, 중간에 429를 여러 번 맞았다면
// 그만큼 여러 번 카운트됩니다("최종적으로 완전히 버려진 주문 수"가 아니라
// "시스템이 순간적으로 거절 신호를 보낸 횟수"). 사용자에게 이 차이를
// 설명했고, 이미 있는 rejected_backpressure 카운터를 그대로 분 단위로
// 버킷화하는 단순한 쪽으로 가기로 확인받았습니다 — "재시도 끝에 완전히
// 포기한 주문만" 세려면 trader/replayengine의 재시도 로직에 새 카운터를
// 추가해야 해서 더 큰 변경이 필요합니다.
//
// 이 데이터는 recorder의 접수/체결 시계열(trade_order/execution 테이블
// 기반, DB에 남음)과 달리 **MySQL에 전혀 남지 않습니다** — 429로 거절된
// 요청은 orderapi가 Kafka에 발행하기도 전에 끝나서 recorder가 볼 방법이
// 없습니다. 그래서 이 프로세스 메모리에만 있는 분 단위 버킷으로 추적하고,
// orderapi 파드가 재시작되면(replicas: 1이라 여러 파드 합산 문제는 없지만)
// 그 이전 데이터는 사라집니다 — recorder의 DB 기반 시계열과 달리 이 값은
// "지금 이 프로세스가 뜬 뒤로 관측한 것"이라는 한계가 있습니다.
const (
	droppedBucket    = time.Minute
	droppedRetention = 24 * time.Hour // 프론트 range picker 최대 옵션(24시간)과 맞춤
)

var (
	droppedMu      sync.Mutex
	droppedBuckets = make(map[int64]int64) // 버킷 시작(UTC, 분 단위 Unix)-> 카운트
)

// recordDroppedOrder는 429(rejected_backpressure) 응답을 보낼 때마다
// 호출합니다(server.go의 ordersTotalCounter.WithLabelValues("rejected_backpressure").Inc()
// 바로 옆).
func recordDroppedOrder() {
	now := time.Now().UTC()
	key := now.Truncate(droppedBucket).Unix()
	cutoff := now.Add(-droppedRetention).Truncate(droppedBucket).Unix()

	droppedMu.Lock()
	defer droppedMu.Unlock()
	droppedBuckets[key]++
	for k := range droppedBuckets {
		if k < cutoff {
			delete(droppedBuckets, k)
		}
	}
}

// droppedBucketDTO는 GET /v1/metrics/dropped-orders 응답의 항목 하나 —
// recorder MetricsBucket과 같은 필드 이름(bucketStart)을 써서 프론트가 두
// 시계열을 bucketStart 키로 그대로 병합할 수 있게 합니다.
type droppedBucketDTO struct {
	BucketStart string `json:"bucketStart"`
	Dropped     int64  `json:"dropped"`
}

// droppedOrdersHandler는 GET /v1/metrics/dropped-orders?from=&to=(둘 다
// RFC3339 필수, recorder GET /v1/metrics/throughput과 동일한 규약)를
// 처리합니다. 라이브 프로세스 메모리 조회라 recorder 쪽과 달리 DB 부하
// 걱정은 없지만, 프론트의 호출 빈도 규칙(자동 폴링 금지, 기간 바꿀 때만)은
// 그대로 따릅니다 — 두 시계열을 같은 화면에서 나란히 그리므로 하나만 자주
// 부르면 오히려 헷갈립니다.
func droppedOrdersHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		reqID := requestID(r)
		w.Header().Set("X-Request-Id", reqID)

		fromStr := r.URL.Query().Get("from")
		toStr := r.URL.Query().Get("to")
		if fromStr == "" || toStr == "" {
			writeError(w, reqID, http.StatusBadRequest, "MISSING_RANGE", "from/to는 둘 다 필수입니다 (RFC3339).")
			return
		}
		from, err := time.Parse(time.RFC3339, fromStr)
		if err != nil {
			writeError(w, reqID, http.StatusBadRequest, "INVALID_FROM", "from 형식이 올바르지 않습니다 (RFC3339).")
			return
		}
		to, err := time.Parse(time.RFC3339, toStr)
		if err != nil {
			writeError(w, reqID, http.StatusBadRequest, "INVALID_TO", "to 형식이 올바르지 않습니다 (RFC3339).")
			return
		}
		if !to.After(from) {
			writeError(w, reqID, http.StatusBadRequest, "INVALID_RANGE", "to는 from보다 이후여야 합니다.")
			return
		}
		if to.Sub(from) > droppedRetention {
			writeError(w, reqID, http.StatusBadRequest, "RANGE_TOO_WIDE", "구간이 너무 깁니다 (최대 "+droppedRetention.String()+").")
			return
		}

		fromKey := from.UTC().Truncate(droppedBucket).Unix()
		toKey := to.UTC().Truncate(droppedBucket).Unix()

		droppedMu.Lock()
		series := make([]droppedBucketDTO, 0, (toKey-fromKey)/int64(droppedBucket.Seconds())+1)
		for k := fromKey; k <= toKey; k += int64(droppedBucket.Seconds()) {
			series = append(series, droppedBucketDTO{
				BucketStart: time.Unix(k, 0).UTC().Format("2006-01-02T15:04:00Z"),
				Dropped:     droppedBuckets[k],
			})
		}
		droppedMu.Unlock()

		writeJSON(w, http.StatusOK, map[string][]droppedBucketDTO{"series": series})
	}
}
