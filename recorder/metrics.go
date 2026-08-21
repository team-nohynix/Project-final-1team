package main

import (
	"context"
	"encoding/json"
	"log"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/redis/go-redis/v9"

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

// dashboardMetricsLockKey/dashboardMetricsCacheKey는 "레코더 레플리카 중 딱
// 하나만 실제로 MySQL을 조회하게" 만드는 Redis 리더 락+캐시입니다(2026-08-21,
// RDS CPU 97~99% 포화 사고 대응) — pollDashboardMetrics는 파티션 분담이 없어서
// 살아있는 레플리카 전부가 각자 10초마다 똑같은 전역 집계 쿼리를 MySQL에
// 날리고 있었고, 레플리카 수만큼 부하가 그대로 곱해지는 게 그 사고의 진짜
// 원인 중 하나였습니다. orderapi/session의 Redis SETNX 락과 같은 패턴입니다.
//
// dashboardMetricsLockTTL을 폴링 주기(10s)보다 짧게(8s) 둔 이유: 리더가
// 살아있는 동안은 자기 다음 주기 시작 시점(정확히 락이 막 만료된 직후)에
// 다시 SETNX해서 계속 리더 자리를 이어받습니다 — 락이 주기보다 길면 그
// 사이클엔 아무도 못 잡아 캐시가 갱신 안 되는 낭비 사이클이 생깁니다.
// 리더가 죽으면 최대 8초 안에 자연히 다른 레플리카가 이어받습니다(self-heal,
// 이 프로젝트의 TTL 기반 안전장치들과 같은 철학).
//
// dashboardMetricsCacheTTL은 주기 몇 번 치의 여유(20s)를 둬서, 리더 교체가
// 살짝 늦어져도 나머지 레플리카가 완전히 빈 값(캐시 없음)을 노출하지 않게
// 합니다.
const (
	dashboardMetricsLockKey  = "recorder:dashboard-metrics:lock"
	dashboardMetricsCacheKey = "recorder:dashboard-metrics:cache"
	dashboardMetricsLockTTL  = 8 * time.Second
	dashboardMetricsCacheTTL = 20 * time.Second
)

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
// 실제 MySQL 조회는 dashboardMetricsForThisCycle이 리더인 레플리카에서만
// 일어나고, 나머지는 그 결과를 Redis 캐시에서 읽어 자기 게이지만 갱신합니다 —
// 그래서 이 함수가 몇 개의 레플리카에서 동시에 돌아도 MySQL 쿼리는 항상
// 주기당 최대 1번입니다.
func pollDashboardMetrics(ctx context.Context, querier *query.MySQLQuerier, redisClient *redis.Client) {
	ticker := time.NewTicker(dashboardMetricsPollInterval)
	defer ticker.Stop()
	for {
		if m, ok := dashboardMetricsForThisCycle(ctx, querier, redisClient); ok {
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

// dashboardMetricsForThisCycle는 이번 주기에 노출할 값을 정합니다. 이
// 인스턴스가 리더 락(SETNX)을 잡으면 직접 MySQL을 조회해 계산하고 Redis
// 캐시에 남긴 뒤 그 값을 반환합니다. 못 잡으면(다른 레플리카가 이미 리더)
// 그 캐시를 읽기만 합니다 — 캐시가 아직 없으면(리더가 막 락만 잡고 아직
// 못 썼거나, 리더 교체 직후) 이번 주기는 조용히 건너뛰고 다음 주기에
// 다시 시도합니다.
func dashboardMetricsForThisCycle(ctx context.Context, querier *query.MySQLQuerier, redisClient *redis.Client) (query.DashboardMetrics, bool) {
	isLeader, err := redisClient.SetNX(ctx, dashboardMetricsLockKey, "1", dashboardMetricsLockTTL).Result()
	if err != nil {
		log.Printf("대시보드 지표 리더 락 확인 실패(이번 주기 건너뜀): %v", err)
		return query.DashboardMetrics{}, false
	}

	if isLeader {
		m, err := querier.DashboardMetrics(ctx)
		if err != nil {
			log.Printf("대시보드 지표 폴링 실패(다음 주기에 재시도): %v", err)
			return query.DashboardMetrics{}, false
		}
		if body, err := json.Marshal(m); err != nil {
			log.Printf("대시보드 지표 캐시 인코딩 실패(이번 주기 값 자체는 정상 노출): %v", err)
		} else if err := redisClient.Set(ctx, dashboardMetricsCacheKey, body, dashboardMetricsCacheTTL).Err(); err != nil {
			log.Printf("대시보드 지표 캐시 저장 실패(이번 주기 값 자체는 정상 노출): %v", err)
		}
		return m, true
	}

	body, err := redisClient.Get(ctx, dashboardMetricsCacheKey).Bytes()
	if err != nil {
		if err != redis.Nil {
			log.Printf("대시보드 지표 캐시 조회 실패(이번 주기 건너뜀): %v", err)
		}
		return query.DashboardMetrics{}, false
	}
	var m query.DashboardMetrics
	if err := json.Unmarshal(body, &m); err != nil {
		log.Printf("대시보드 지표 캐시 파싱 실패(이번 주기 건너뜀): %v", err)
		return query.DashboardMetrics{}, false
	}
	return m, true
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
