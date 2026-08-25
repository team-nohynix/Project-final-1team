package main

import (
	"context"
	"crypto/tls"
	"database/sql"
	"log"
	"net/http"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/redis/go-redis/v9"

	"recorder/archive"
	"recorder/backpressure"
	"recorder/events"
	rkafka "recorder/kafka"
	"recorder/query"
	"recorder/store"
)

// 아카이브 마이크로배치는 건수 또는 시간 중 먼저 도달하면 플러시합니다 —
// matching/engine.Engine의 스냅샷 이중 트리거와 같은 패턴. orders/executions는
// 각자 별도 Batcher를 써서 한쪽이 몰려도 다른 쪽 플러시 주기가 밀리지 않습니다.
const (
	archiveFlushEvery    = 500
	archiveFlushInterval = 30 * time.Second

	// 컨슈머 그룹 ID는 고정 상수입니다 — matching과 마찬가지로 배포 전체가
	// 공유해야 하는 값이라 환경변수가 아닙니다. orders/executions/assignments를
	// 별도 그룹으로 나눠서 서로의 파티션 배정에 관여하지 않게 합니다.
	ordersGroupID      = "recorder-orders"
	executionsGroupID  = "recorder-executions"
	assignmentsGroupID = "recorder-assignments"

	// RDS 백프레셔 워터마크(2026-08-07, CLAUDE.md의 "RDS admission control via
	// recorder consumer lag" 참고). High/Low를 다르게 둔 이유는
	// backpressure.Watcher의 히스테리시스 설명 참고.
	//
	// 2026-08-11 실측 후 하향 조정(5000/1000 → 2000/400): 실제 부하테스트 중
	// orders/executions 두 리더가 같은 trade_order 행을 동시에 건드리면서 진짜
	// MySQL 데드락이 나는 걸 발견했습니다(재시도 로직은 추가해서 죽지는 않게
	// 고쳤음, store/mysql.go의 withRetryOnDeadlock 참고) — 그런데 데드락이
	// 아니더라도 두 리더 간 락 경합 자체가 남아있어서, 한번 크게 밀리면
	// (특히 executions 쪽) 회복이 예상보다 훨씬 느릴 수 있다는 걸 확인했습니다.
	// 이 경합의 정확한 한계치까지는 아직 못 쟀지만(실측이 극단적인 인위적
	// 백로그 상황이라 일반화하기 어려움), "더 못 재서 낮췄다"보다는 "밀리기
	// 시작하는 시점을 앞당겨서 큰 동시 백로그가 애초에 잘 안 쌓이게" 하는
	// 쪽이 안전하다고 판단해 보수적으로 낮췄습니다. 여전히 잠정값이고, 정확한
	// 한계치는 락 경합 자체를 더 깊이 고치거나(팀 결정: 이번엔 보류) 더 정교한
	// 정상 부하 재현 테스트로 재조정할 여지가 있습니다.
	backpressureHighWatermark = 2000
	backpressureLowWatermark  = 400
	backpressureCheckInterval = 5 * time.Second
	backpressureRedisKey      = "backpressure:recorder_lag"
)

func main() {
	cfg := LoadConfig()
	ctx := context.Background()

	db, err := sql.Open("mysql", cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("MySQL 드라이버 초기화 실패: %v", err)
	}
	defer db.Close()
	// sql.Open은 실제로 연결하지 않고 지연 연결하므로, 시작 시점에 곧바로 확인해서
	// (연결 문자열이 잘못됐거나 DB가 안 떠 있으면) 여기서 바로 실패가 드러나게 합니다.
	if err := db.PingContext(ctx); err != nil {
		log.Fatalf("MySQL 연결 확인 실패: %v", err)
	}
	dbStore := store.NewMySQLStore(db)

	var archiveStore archive.Store
	if cfg.ArchiveBucket != "" {
		archiveStore = archive.NewS3Store(cfg.ArchiveBucket)
	} else {
		archiveStore = archive.NewLocalStore("records")
	}
	orderBatcher := archive.NewBatcher(archiveStore, "orders", archiveFlushEvery)
	execBatcher := archive.NewBatcher(archiveStore, "executions", archiveFlushEvery)
	go runPeriodicFlush(ctx, orderBatcher, archiveFlushInterval)
	go runPeriodicFlush(ctx, execBatcher, archiveFlushInterval)

	orderReader, err := rkafka.NewOrderReader(ctx, cfg.KafkaBroker, cfg.OrdersTopic, ordersGroupID, cfg.KafkaUseIAMAuth)
	if err != nil {
		log.Fatalf("orders 리더 생성 실패: %v", err)
	}
	defer orderReader.Close()
	execReader, err := rkafka.NewExecutionReader(ctx, cfg.KafkaBroker, cfg.ExecutionsTopic, executionsGroupID, cfg.KafkaUseIAMAuth)
	if err != nil {
		log.Fatalf("executions 리더 생성 실패: %v", err)
	}
	defer execReader.Close()
	assignmentReader, err := rkafka.NewAssignmentReader(ctx, cfg.KafkaBroker, cfg.AssignmentsTopic, assignmentsGroupID, cfg.KafkaUseIAMAuth)
	if err != nil {
		log.Fatalf("assignments 리더 생성 실패: %v", err)
	}
	defer assignmentReader.Close()

	redisOpts := &redis.Options{Addr: cfg.RedisAddr, Password: cfg.RedisPassword}
	if cfg.RedisTLSEnabled {
		redisOpts.TLSConfig = &tls.Config{}
	}
	redisClient := redis.NewClient(redisOpts)
	defer redisClient.Close()

	// RDS 백프레셔 감시: orders/executions 리더의 랙을 주기적으로 확인해 Redis
	// 플래그를 켜고 끕니다 — orderapi/backpressure.RedisChecker가 이 플래그를
	// 읽어 신규 주문을 429로 거절할지 결정합니다(기록기는 이 결정에 관여하지
	// 않고 오직 관찰한 사실만 Redis에 남김).
	watcher := &backpressure.Watcher{
		Sources: []backpressure.LagSource{
			instrumentedLagSource("orders", orderReader.Lag),
			instrumentedLagSource("executions", execReader.Lag),
		},
		Flag:          instrumentedFlag{inner: &backpressure.RedisFlag{Client: redisClient, Key: backpressureRedisKey}},
		HighWatermark: backpressureHighWatermark,
		LowWatermark:  backpressureLowWatermark,
		CheckInterval: backpressureCheckInterval,
	}
	go watcher.Run(ctx)

	// 조회 전용 HTTP 서버(2026-08-12 추가, docs/frontend-backend-integration.md
	// 참고) — 기록기가 이미 RDS에 쌓아둔 데이터를 읽기만 하고, Kafka 컨슈머
	// 루프와는 완전히 독립적으로 별도 고루틴에서 돕니다.
	querier := query.NewMySQLQuerier(db)
	go pollDashboardMetrics(ctx, querier, redisClient)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/trace/{orderId}", traceHandler(querier))
	mux.HandleFunc("GET /v1/matching/engines", enginesHandler(querier))
	mux.HandleFunc("GET /v1/metrics/dashboard", dashboardMetricsHandler(querier, redisClient))
	mux.HandleFunc("GET /v1/metrics/throughput", throughputHandler(querier))
	mux.HandleFunc("GET /v1/health", healthHandler(db))
	mux.HandleFunc("GET /v1/orders/summary", orderSummaryHandler(querier))
	mux.HandleFunc("GET /v1/orders/unresolved", unresolvedOrdersHandler(querier))
	mux.HandleFunc("GET /v1/orders/unresolved/all", allUnresolvedOrdersHandler(querier))
	mux.HandleFunc("GET /v1/orders/integrity", integrityCheckHandler(querier))

	mux.Handle("GET /metrics", promhttp.Handler())
	go func() {
		addr := ":" + cfg.Port
		log.Printf("기록기 조회 API 시작: %s", addr)
		log.Fatal(http.ListenAndServe(addr, mux))
	}()

	archiveDest := cfg.ArchiveBucket
	if archiveDest == "" {
		archiveDest = "(로컬 ./records)"
	}
	log.Printf("기록기 시작 (broker=%s, orders=%s, executions=%s, assignments=%s, archive=%s, redis=%s)",
		cfg.KafkaBroker, cfg.OrdersTopic, cfg.ExecutionsTopic, cfg.AssignmentsTopic, archiveDest, cfg.RedisAddr)

	// execReader.Run/orderReader.Run은 이제 메시지를 배치로 모아 넘겨줍니다
	// (RDS 백프레셔 대응 배칭, 2026-08-07). S3 아카이브 배치(execBatcher/
	// orderBatcher)는 원래부터 인메모리 버퍼링이라 굳이 같이 안 묶어도 저렴하므로,
	// RDS 배치 하나당 건별로 계속 Add합니다 — RDS 왕복 배칭과 S3 아카이브
	// 배칭은 서로 다른 목적의, 독립적인 두 배치입니다(크기/주기도 다름).
	go func() {
		err := execReader.Run(ctx, func(ctx context.Context, evs []events.ExecutionEvent) error {
			for _, ev := range evs {
				execBatcher.Add(ev)
			}
			applied, err := store.ApplyExecutionEvents(ctx, dbStore, evs)
			if err != nil {
				return err
			}
			// DB 커밋이 실제로 성공했을 때만(err == nil) 카운터를 늘립니다 —
			// 실패했으면 이 배치는 오프셋 미커밋으로 통째로 재시도되므로,
			// 여기서 먼저 늘리면 재시도 성공 시 중복 집계가 됩니다
			// (recorder/metrics.go, store/apply.go의 ApplyExecutionEvents 참고).
			recorderExecutionTotal.Add(float64(applied))
			return nil
		})
		log.Fatalf("executions 리더 종료: %v", err)
	}()

	go func() {
		err := assignmentReader.Run(ctx, func(ctx context.Context, ev events.AssignmentEvent) error {
			return store.ApplyAssignmentEvent(ctx, dbStore, ev)
		})
		log.Fatalf("assignments 리더 종료: %v", err)
	}()

	err = orderReader.Run(ctx, func(ctx context.Context, evs []events.OrderEvent) error {
		for _, ev := range evs {
			orderBatcher.Add(ev)
		}
		accepted, err := store.ApplyOrderEvents(ctx, dbStore, evs)
		if err != nil {
			return err
		}
		// execReader 콜백과 같은 이유로 성공했을 때만 늘립니다.
		recorderOrderAcceptTotal.Add(float64(accepted))
		return nil
	})
	log.Fatalf("orders 리더 종료: %v", err)
}

func runPeriodicFlush(ctx context.Context, b *archive.Batcher, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			b.Flush()
		}
	}
}
