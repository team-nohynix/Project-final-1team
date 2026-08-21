package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"sync/atomic"

	"matching/backpressure"
)

// backpressureActive는 매칭 자체 백프레셔(main.go의 matchingWatcher)가 마지막으로
// 판단한 활성 여부입니다 — Watcher goroutine이 쓰고 /metrics 핸들러 goroutine이
// 읽으므로 atomic으로 공유합니다. 이 값이 없으면(2026-08-13 발견) recorder처럼
// 매칭도 백프레셔 상태 전환이 로그에만 남고 Grafana에서는 전혀 안 보였습니다.
var backpressureActive atomic.Bool

// marketAcquired/ReleasedCounter는 FR-11 리밸런싱(마켓을 다른 인스턴스로 넘기고
// 받는 일)이 실제로 얼마나 자주 일어나는지 셉니다 — 지금까지는 assignments 토픽
// 이벤트로만 남아서 recorder DB를 조회해야 알 수 있었습니다.
var (
	marketAcquiredCounter atomic.Uint64
	marketReleasedCounter atomic.Uint64
)

// instrumentedFlag는 backpressure.FlagSetter를 감싸서 실제 활성 상태가 바뀔 때마다
// backpressureActive도 같이 갱신합니다.
type instrumentedFlag struct {
	inner backpressure.FlagSetter
}

func (f instrumentedFlag) SetActive(ctx context.Context, active bool) error {
	backpressureActive.Store(active)
	return f.inner.SetActive(ctx, active)
}

// startMetricsServer는 Prometheus 텍스트 노출 포맷으로 /metrics를 서빙합니다.
// 별도 클라이언트 라이브러리 없이 손으로 포맷을 씁니다 — 게이지 몇 개뿐이라
// 의존성을 추가할 이유가 없습니다(이 저장소의 dev-simple 관례와 같은 이유).
// lagFn/bookSizeFn은 매 스크레이프마다 그대로 호출됩니다(둘 다 이미 담당 중인
// 파티션/마켓들을 훑는 가벼운 연산이라 캐싱이 필요 없습니다) — 랙은 "아직 못
// 읽은 메시지 수", 북사이즈는 "다 읽었지만 체결 상대가 없어 메모리에 쌓인
// 미체결 주문 수"라 서로 다른 종류의 부하를 잽니다(2026-08-21, 랙이 0인데도
// OOM이 난 사고에서 랙만으로는 이 누적을 못 본다는 게 드러나 추가).
func startMetricsServer(port string, lagFn func() int64, bookSizeFn func() int) {
	mux := http.NewServeMux()
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		fmt.Fprintf(w, "# HELP matching_engine_lag 이 인스턴스가 담당 중인 파티션들의 컨슈머 랙 합계 (orders 토픽, backpressure.RedisFlag와 같은 값)\n")
		fmt.Fprintf(w, "# TYPE matching_engine_lag gauge\n")
		fmt.Fprintf(w, "matching_engine_lag %d\n", lagFn())
		fmt.Fprintf(w, "# HELP matching_engine_book_size 이 인스턴스가 담당 중인 모든 마켓의 호가창 미체결 주문 수 합계 (체결 상대가 없어 쌓이기만 하는 양 — 컨슈머 랙과 달리 이미 읽은 메시지의 결과임)\n")
		fmt.Fprintf(w, "# TYPE matching_engine_book_size gauge\n")
		fmt.Fprintf(w, "matching_engine_book_size %d\n", bookSizeFn())
		fmt.Fprintf(w, "# HELP matching_backpressure_active 매칭 자체 백프레셔 활성 여부 (1=활성)\n")
		fmt.Fprintf(w, "# TYPE matching_backpressure_active gauge\n")
		active := 0
		if backpressureActive.Load() {
			active = 1
		}
		fmt.Fprintf(w, "matching_backpressure_active %d\n", active)
		fmt.Fprintf(w, "# HELP matching_market_assignments_total 마켓 배정/반납 누적 횟수 (FR-11 리밸런싱)\n")
		fmt.Fprintf(w, "# TYPE matching_market_assignments_total counter\n")
		fmt.Fprintf(w, "matching_market_assignments_total{action=\"acquired\"} %d\n", marketAcquiredCounter.Load())
		fmt.Fprintf(w, "matching_market_assignments_total{action=\"released\"} %d\n", marketReleasedCounter.Load())
	})

	go func() {
		addr := ":" + port
		log.Printf("메트릭 서버 시작 (:%s/metrics)", port)
		if err := http.ListenAndServe(addr, mux); err != nil {
			log.Printf("메트릭 서버 종료: %v", err)
		}
	}()
}
