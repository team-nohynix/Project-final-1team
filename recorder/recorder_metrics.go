package main

import (
	"log"
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	recordsProcessedCounter = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "recorder_records_processed_total",
			Help: "Total records processed",
		},
		[]string{"type"},
	)
)

func init() {
	prometheus.MustRegister(recordsProcessedCounter)
}

func startMetricsServer(port string) {
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", promhttp.Handler())
	go func() {
		addr := ":" + port
		log.Printf("Recorder 메트릭 서버 시작: %s", addr)
		if err := http.ListenAndServe(addr, mux); err != nil {
			log.Printf("메트릭 서버 에러: %v", err)
		}
	}()
}
