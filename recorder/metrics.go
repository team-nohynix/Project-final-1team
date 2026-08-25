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

// sessionLastRunKey는 orderapi/session.go의 lastRunKey와 정확히 같은 값이어야
// 합니다(모듈 간 타입 비공유 원칙 — 값만 맞추면 됨). orderapi/recorder가 같은
// Redis를 공유해서, orderapi가 세션 시작/종료 때 쓰는 이 키를 recorder도 읽을
// 수 있습니다 — currentDashboardWindow(아래)가 씁니다.
const sessionLastRunKey = "orderapi:session:lastrun"

// sessionLastRunRecord는 orderapi/session.RunRecord에서 recorder가 필요한
// 필드만 복사한 것입니다(json 태그가 없는 구조체라 Go 기본 필드명 그대로
// 직렬화됩니다 — 이름만 맞으면 됩니다).
type sessionLastRunRecord struct {
	Status    string
	StartedAt time.Time
	EndedAt   time.Time
}

// currentDashboardWindow는 "진행 중인 테스트가 없을 땐 최근 N분 롤링 창 대신
// 마지막 실행 구간을 보여주는 게 더 유용하다"는 요청(2026-08-25) 지원 —
// TPS/p99를 그 실행의 [시작,종료) 구간 전체로 계산하기 위한 구간을 정합니다.
// 다음 경우엔 nil을 돌려줘서 DashboardMetrics가 기본 롤링 창을 쓰게 둡니다:
// 진행 중(IN_PROGRESS — 방금 몇 초/분의 실시간 처리량이 의미 있는 시점이라
// 오히려 롤링 창이 맞음), 세션 키 자체가 없음(한 번도 실행된 적 없음),
// EndedAt이 비어있음(정상 반납 없이 죽은 좀비 기록 — 구간을 알 수 없음).
func currentDashboardWindow(ctx context.Context, redisClient *redis.Client) *query.TimeWindow {
	body, err := redisClient.Get(ctx, sessionLastRunKey).Bytes()
	if err != nil {
		return nil
	}
	var rec sessionLastRunRecord
	if err := json.Unmarshal(body, &rec); err != nil {
		return nil
	}
	if rec.Status == "IN_PROGRESS" || rec.EndedAt.IsZero() {
		return nil
	}
	return &query.TimeWindow{From: rec.StartedAt, To: rec.EndedAt}
}

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
	// recorderOrdersByStatus는 2026-08-21 재도입 — query.DashboardMetrics.OrdersByStatus
	// 타입 주석 참고. 예전엔 status만으로 GROUP BY(시간 제한 없음)해서 RDS CPU
	// 포화 사고의 원인이 됐던 지표라, 이번엔 반드시 이 리더 락+캐시 폴링
	// 경로로만 갱신됩니다(레플리카별 중복 쿼리 방지) — 다른 경로로 직접
	// 쿼리해서 이 게이지를 갱신하는 코드를 추가하면 안 됩니다.
	recorderOrdersByStatus = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "recorder_orders_by_status",
		Help: "최근 ordersByStatusWindow(10분) 안에 접수된 주문의 상태별 건수",
	}, []string{"status"})
)

// recorderOrderAcceptTotal/recorderExecutionTotal은 2026-08-21 추가 — 위
// recorderOrderAcceptTps/recorderExecutionTps(둘 다 pollDashboardMetrics가
// 10초마다 MySQL을 다시 읽어서 채움)와 달리, DB를 전혀 안 읽고 recorder가
// Kafka 배치를 실제로 처리하는 바로 그 지점(main.go의 orderReader.Run/
// execReader.Run 콜백)에서 직접 증가시키는 순수 카운터입니다. TPS 값 자체는
// 여기서 계산하지 않고 Prometheus/Grafana의 rate()에 맡깁니다 — 이게
// Prometheus가 이런 지표를 위해 설계된 표준 방식이고, recorder 쪽에서
// 슬라이딩 윈도우를 직접 구현할 필요가 없어집니다.
//
// **레플리카가 여러 개일 때의 집계 방식이 recorder_order_accept_tps와
// 다릅니다** — recorder_order_accept_tps는 Redis 리더 락으로 레플리카 중
// 하나만 전체 집계를 계산해 모든 레플리카가 같은 값을 노출하지만(2026-08-21
// RDS CPU 포화 사고 대응으로 추가된 캐시), 이 카운터들은 Kafka 파티션 배정을
// 통해 원래 마켓별로 나뉘어 처리되는 각자 자기 몫만 셉니다(레플리카 간
// 조율 없음) — 전체 시스템 TPS를 보려면 Grafana 쪽에서
// sum(rate(recorder_order_accept_total[1m]))처럼 레플리카 전체를 합산해야
// 합니다(Prometheus 다중 인스턴스 카운터의 표준 집계 방식). 기존
// recorder_order_accept_tps 패널을 이걸로 옮기려면 이 sum(rate(...))
// 형태로 쿼리를 바꿔야 합니다 — Grafana 쪽 변경이 필요하므로 별도 확인 필요.
//
// PendingOrders(미종결 건수)는 일부러 카운터로 안 옮겼습니다 — 접수/체결/
// 취소/부분체결/취소가 뒤늦은 체결로 정정되는 경우까지 상태 전이가
// store/mysql.go의 applyFillsBatch/CancelOrdersBatch/reconcilePreexistingFills에
// 여러 갈래로 나뉘어 있어서, 카운터 증감 로직을 정확히 똑같이 복제하지
// 않으면 조용히 틀린 값이 나올 위험이 큽니다 — 지금은 그 정확성 리스크가
// "10초마다 인덱스 탄 쿼리 한 번"이라는 이미 저렴해진 비용보다 크다고
// 판단해 SQL 기반(+리더 락 캐시)을 그대로 둡니다.
var (
	recorderOrderAcceptTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "recorder_order_accept_total",
		Help: "이 레플리카가 실제로 저장한 신규 주문 누적 건수(재전달로 스킵된 중복 제외, DB 재조회 없이 실시간 증가) — 전체 TPS는 sum(rate(recorder_order_accept_total[1m]))로 계산",
	})
	recorderExecutionTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "recorder_execution_total",
		Help: "이 레플리카가 실제로 저장한 체결 누적 건수(DB 재조회 없이 실시간 증가) — 전체 TPS는 sum(rate(recorder_execution_total[1m]))로 계산",
	})
)

func init() {
	prometheus.MustRegister(
		recorderLagGauge, recorderBackpressureActiveGauge,
		recorderOrderAcceptTps, recorderExecutionTps, recorderPendingOrders,
		recorderE2EP99Ms, recorderE2EP99SampleCount, recorderRunningEnginePods,
		recorderOrderAcceptTotal, recorderExecutionTotal, recorderOrdersByStatus,
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
			for status, count := range m.OrdersByStatus {
				recorderOrdersByStatus.WithLabelValues(status).Set(float64(count))
			}
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
		m, err := querier.DashboardMetrics(ctx, currentDashboardWindow(ctx, redisClient))
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
