package main

import (
	"context"

	"github.com/prometheus/client_golang/prometheus"

	"recorder/backpressure"
)

// recorderLagGauge/recorderBackpressureActiveGauge는 백프레셔 Watcher(main.go)가 이미
// 계산 중인 값을 Prometheus로도 내보냅니다 — 이게 없으면 이 값들은 로그(백프레셔 상태
// 전환 라인)로만 남고 Grafana에서는 전혀 안 보였습니다(2026-08-13, 부하테스트 중 recorder가
// 실제 병목인데도 대시보드에는 아무 신호가 없어서 로그를 직접 봐야 알 수 있었던 문제).
var (
	recorderLagGauge = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "recorder_consumer_lag",
			Help: "기록기 컨슈머 랙(orders/executions 리더별)",
		},
		[]string{"reader"},
	)
	recorderBackpressureActiveGauge = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "recorder_backpressure_active",
			Help: "RDS 백프레셔 활성 여부 (1=활성, orderapi가 신규 주문을 429로 거절 중)",
		},
	)
)

func init() {
	prometheus.MustRegister(recorderLagGauge, recorderBackpressureActiveGauge)
}

// instrumentedLagSource는 Watcher가 원래 쓰는 LagSource를 감싸서, Watcher가 값을
// 읽는 바로 그 순간에 게이지도 같이 갱신합니다 — 별도로 Kafka 랙을 다시 조회하지
// 않고 Watcher의 기존 폴링 주기에 얹혀갑니다.
func instrumentedLagSource(reader string, src backpressure.LagSource) backpressure.LagSource {
	return func() int64 {
		lag := src()
		recorderLagGauge.WithLabelValues(reader).Set(float64(lag))
		return lag
	}
}

// instrumentedFlag는 Watcher.Flag(Redis에 백프레셔 상태를 쓰는 쪽)를 감싸서, 실제
// 상태가 바뀔 때마다 게이지도 같이 갱신합니다.
type instrumentedFlag struct {
	inner backpressure.FlagSetter
}

func (f instrumentedFlag) SetActive(ctx context.Context, active bool) error {
	v := 0.0
	if active {
		v = 1
	}
	recorderBackpressureActiveGauge.Set(v)
	return f.inner.SetActive(ctx, active)
}
