package main

import (
	"context"
	"log"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"recorder/backpressure"
	"recorder/query"
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

// dashboardMetricsPollInterval은 GET /v1/metrics/dashboard가 이미 계산하는 값들을
// 그대로 재사용해 Grafana에도 노출하기 위한 폴링 주기입니다(2026-08-19, 프론트
// "실시간 모니터링" 화면과 동일한 지표를 Grafana에도 띄워달라는 요청으로 추가) —
// TPS/대기주문/p99 계산이 전부 MySQL 쿼리라 매 스크레이프(보통 15s)마다 다시
// 돌리면 DB 부하가 늘어나므로, 별도 주기로 값을 캐싱하듯 갱신합니다.
const dashboardMetricsPollInterval = 10 * time.Second

var (
	recorderOrderAcceptTps = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "recorder_order_accept_tps",
		Help: "최근 60초 주문 접수 TPS (trade_order.submitted_at 기준)",
	})
	recorderExecutionTps = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "recorder_execution_tps",
		Help: "최근 60초 체결 TPS (execution.executed_at 기준)",
	})
	recorderPendingOrders = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "recorder_pending_orders",
		Help: "미종결 주문 수 (ACCEPTED + PARTIALLY_FILLED)",
	})
	recorderE2EP99Ms = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "recorder_e2e_p99_latency_ms",
		Help: "최근 5분 내 체결 완료 주문의 접수→최종체결 p99 지연시간(ms) — 근사치, query.DashboardMetrics 타입 주석 참고",
	})
	recorderE2EP99SampleCount = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "recorder_e2e_p99_sample_count",
		Help: "recorder_e2e_p99_latency_ms 계산에 쓰인 표본 수 — 너무 적으면 p99 값을 신뢰하기 어려움",
	})
	recorderRunningEnginePods = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "recorder_running_engine_pods",
		Help: "현재 마켓이 배정된 매칭 엔진 인스턴스 수(distinct engine_instance_id)",
	})
)

func init() {
	prometheus.MustRegister(
		recorderLagGauge, recorderBackpressureActiveGauge,
		recorderOrderAcceptTps, recorderExecutionTps, recorderPendingOrders,
		recorderE2EP99Ms, recorderE2EP99SampleCount, recorderRunningEnginePods,
	)
}

// pollDashboardMetrics는 DashboardMetrics(GET /v1/metrics/dashboard가 쓰는 것과 같은
// 함수)를 주기적으로 호출해 위 게이지들을 갱신합니다. ctx가 취소되면 종료합니다.
func pollDashboardMetrics(ctx context.Context, querier *query.MySQLQuerier) {
	ticker := time.NewTicker(dashboardMetricsPollInterval)
	defer ticker.Stop()
	for {
		m, err := querier.DashboardMetrics(ctx)
		if err != nil {
			log.Printf("대시보드 지표 폴링 실패(다음 주기에 재시도): %v", err)
		} else {
			recorderOrderAcceptTps.Set(m.OrderAcceptTps)
			recorderExecutionTps.Set(m.ExecutionTps)
			recorderPendingOrders.Set(float64(m.PendingOrders))
			recorderE2EP99Ms.Set(m.E2EP99Ms)
			recorderE2EP99SampleCount.Set(float64(m.E2EP99SampleCount))
			recorderRunningEnginePods.Set(float64(m.RunningEnginePods))
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
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
