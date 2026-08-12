package main

import (
	"fmt"
	"log"
	"net/http"
)

// startMetricsServer는 Prometheus 텍스트 노출 포맷으로 /metrics를 서빙합니다.
// 별도 클라이언트 라이브러리 없이 손으로 포맷을 씁니다 — 게이지 하나뿐이라
// 의존성을 추가할 이유가 없습니다(이 저장소의 dev-simple 관례와 같은 이유).
// lagFn은 매 스크레이프마다 그대로 호출됩니다(GroupConsumer.Lag는 이미 담당 중인
// 파티션들의 합계를 훑는 가벼운 연산이라 캐싱이 필요 없습니다).
func startMetricsServer(port string, lagFn func() int64) {
	mux := http.NewServeMux()
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		fmt.Fprintf(w, "# HELP matching_engine_lag 이 인스턴스가 담당 중인 파티션들의 컨슈머 랙 합계 (orders 토픽, backpressure.RedisFlag와 같은 값)\n")
		fmt.Fprintf(w, "# TYPE matching_engine_lag gauge\n")
		fmt.Fprintf(w, "matching_engine_lag %d\n", lagFn())
	})

	go func() {
		addr := ":" + port
		log.Printf("메트릭 서버 시작 (:%s/metrics)", port)
		if err := http.ListenAndServe(addr, mux); err != nil {
			log.Printf("메트릭 서버 종료: %v", err)
		}
	}()
}
