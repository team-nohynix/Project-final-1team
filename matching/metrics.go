package main

import (
	"log"
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// startMetricsServer는 Prometheus /metrics 엔드포인트를 서빙합니다.
// lagFn은 매 스크레이프마다 호출되어 matching_engine_lag 게이지를 업데이트합니다.
func startMetricsServer(port string, lagFn func() int64) {
	gaugeMetric := prometheus.NewGaugeFunc(
		prometheus.GaugeOpts{
			Name: "matching_engine_lag",
			Help: "컨슈머 랙 (orders 토픽)",
		},
		func() float64 {
			return float64(lagFn())
		},
	)
	prometheus.MustRegister(gaugeMetric)

	mux := http.NewServeMux()
	mux.Handle("GET /metrics", promhttp.Handler())

	go func() {
		addr := ":" + port
		log.Printf("메트릭 서버 시작 (:%s/metrics)", port)
		if err := http.ListenAndServe(addr, mux); err != nil {
			log.Printf("메트릭 서버 종료: %v", err)
		}
	}()
}
