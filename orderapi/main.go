package main

import (
	"context"
	"crypto/tls"
	"log"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/redis/go-redis/v9"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"orderapi/backpressure"
	"orderapi/idempotency"
	"orderapi/jobtrigger"
	"orderapi/kafkaclient"
	"orderapi/order"
	"orderapi/orderrecords"
	"orderapi/session"
)

// backpressureRecorderLagKey/backpressureMatchingLagKey는 각각
// recorder/backpressure.RedisFlag와 matching/backpressure.RedisFlag가 쓰는
// 것과 반드시 같은 값이어야 합니다(모듈 간 타입 비공유 원칙 — 값만 맞추면
// 됨). 두 병목은 서로 독립적이라(recorder→RDS 적체 vs matching 자체가 orders
// 토픽을 못 따라가는 것) 별도 키로 둡니다 — MultiChecker가 둘 다 확인합니다.
const (
	backpressureRecorderLagKey = "backpressure:recorder_lag"
	backpressureMatchingLagKey = "backpressure:matching_lag"

	// matchingEngineNamespace/matchingEngineDeploymentName은 resetMatchingEngineBookHandler가
	// rollout restart를 걸 대상입니다 — infra/k8s/backend/matching-engine-deployment.yaml의
	// metadata와 반드시 같은 값이어야 합니다.
	matchingEngineNamespace       = "backend"
	matchingEngineDeploymentName  = "matching-engine"
)

// sessionTTL은 세션 락의 Redis 만료 시간입니다 — 클라이언트(trader/replayengine)는
// 이 값의 1/3 주기로 하트비트를 보내야 하고(session.Client.Claim이 응답에 실어주는
// ttlSeconds를 그대로 씀), 크래시로 하트비트가 끊기면 이 시간 뒤 자동으로 풀립니다.
const sessionTTL = 30 * time.Second

func main() {
	cfg := LoadConfig()

	ctx := context.Background()

	store := order.NewStore()
	idem := idempotency.NewStore()
	producer, err := kafkaclient.NewOrderProducer(ctx, cfg.KafkaBroker, cfg.OrdersTopic, cfg.KafkaUseIAMAuth)
	if err != nil {
		log.Fatalf("주문 프로듀서 생성 실패: %v", err)
	}
	defer producer.Close()

	// executions를 구독해 order.Store가 체결을 반영합니다(2026-08-10) —
	// 이게 없으면 cancelOrderHandler의 ORDER_ALREADY_FILLED 체크가 데이터
	// 소스가 없어 무의미했습니다(CLAUDE.md 참고). matching/recorder처럼 별도
	// 컨슈머 그룹("orderapi-executions")을 씁니다. BuyOrderID/SellOrderID
	// 둘 다 시도합니다 — 이 orderapi 인스턴스가 접수한 쪽만 Store에 있으므로
	// (반대편은 이미 orderapi가 재시작됐거나 원래 이 인스턴스가 접수한 게
	// 아닐 수 있음) 한쪽만 찾아지는 것도 정상입니다.
	execConsumer, err := kafkaclient.NewExecutionConsumer(ctx, cfg.KafkaBroker, cfg.ExecutionsTopic, "orderapi-executions", cfg.KafkaUseIAMAuth)
	if err != nil {
		log.Fatalf("executions 컨슈머 생성 실패: %v", err)
	}
	defer execConsumer.Close()
	go func() {
		err := execConsumer.Run(context.Background(), func(ctx context.Context, ev kafkaclient.ExecutionEvent) error {
			store.ApplyFill(ev.BuyOrderID, ev.Quantity)
			store.ApplyFill(ev.SellOrderID, ev.Quantity)
			return nil
		})
		log.Fatalf("executions 컨슈머 종료: %v", err)
	}()

	redisOpts := &redis.Options{Addr: cfg.RedisAddr, Password: cfg.RedisPassword}
	if cfg.RedisTLSEnabled {
		redisOpts.TLSConfig = &tls.Config{}
	}
	redisClient := redis.NewClient(redisOpts)
	defer redisClient.Close()

	sessionStore := session.NewRedisStore(redisClient, sessionTTL)
	// matchingLagChecker는 acceptOrderHandler의 lagChecker(recorder_lag +
	// matching_lag 둘 다 봄)와 별개로 matching_lag 하나만 봅니다 — 미종결
	// 주문 정리(자동/수동 둘 다, sessioncleanup.go 참고)가 시작 전에 확인하는
	// 건 "매칭 엔진이 지금 orders를 잘 따라가고 있는가" 하나뿐이라서입니다.
	matchingLagChecker := &backpressure.RedisChecker{Client: redisClient, Key: backpressureMatchingLagKey}
	lagChecker := backpressure.MultiChecker{
		&backpressure.RedisChecker{Client: redisClient, Key: backpressureRecorderLagKey},
		matchingLagChecker,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/orders", acceptOrderHandler(store, idem, producer, lagChecker))
	mux.HandleFunc("DELETE /v1/orders/{orderId}", cancelOrderHandler(store, producer))
	mux.HandleFunc("GET /v1/markets/{market}/orderbook", orderbookHandler(redisClient))
	mux.HandleFunc("POST /v1/sessions", claimSessionHandler(sessionStore))
	mux.HandleFunc("PUT /v1/sessions/{sessionId}/heartbeat", heartbeatSessionHandler(sessionStore))
	// 프론트 "중지" 버튼(2026-08-20) — sessionId가 아니라 runId로 받습니다(sessionhandlers.go 참고).
	mux.HandleFunc("POST /v1/sessions/{runId}/stop", stopRunHandler(sessionStore))
	// 세션 종료 시 미종결 주문 정리(2026-08-19, sessioncleanup.go 참고)가
	// recorder를 부르는 데 씁니다 — 별도 요청마다 새 클라이언트를 만들 필요
	// 없이 하나 공유합니다.
	recorderHTTPClient := &http.Client{Timeout: 30 * time.Second}
	mux.HandleFunc("DELETE /v1/sessions/{sessionId}", releaseSessionHandler(sessionStore, producer, recorderHTTPClient, cfg.RecorderURL, matchingLagChecker))
	mux.HandleFunc("GET /v1/sessions/last-run", lastRunHandler(sessionStore))
	mux.HandleFunc("GET /v1/sessions/previous-run", previousRunHandler(sessionStore))

	// RECORDER_URL이 없으면(로컬 개발 등) 이 라우트들도 등록하지 않습니다 —
	// cleanupUnresolvedOrders(세션 종료 자동 정리)와 같은 전제(config.go 참고).
	// 일괄 정리 자체는 이제 비동기라(2026-08-20, sessioncleanup.go 참고) 이
	// 클라이언트의 타임아웃은 더 이상 "얼마나 오래 걸리는 작업을 견디느냐"가
	// 아니라 평범한 HTTP 호출(recorder 조회 하나)의 타임아웃일 뿐입니다.
	//
	// 꺼져있을 때도 반드시 로그를 남깁니다(2026-08-20) — JOB_TRIGGER_QUEUE_URL은
	// 원래부터 이렇게 로그를 남기는데 RECORDER_URL만 조용히 넘어가고 있었고,
	// 그래서 실제로 prod 배포 매니페스트에 이 값이 빠졌을 때(세션 종료 자동
	// 정리가 계속 무동작) 아무 신호가 없어서 미체결 주문이 쌓이는 걸 보고
	// 나서야 원인을 알아낸 사고가 있었습니다. "선택 기능으로 둔다"는 판단은
	// 맞지만 "꺼져 있다는 사실"은 시작 로그에 항상 드러나야 합니다.
	if cfg.RecorderURL != "" {
		cleanupHTTPClient := &http.Client{Timeout: 30 * time.Second}
		mux.HandleFunc("POST /v1/admin/cleanup-unresolved-orders", startCleanupAllUnresolvedOrdersHandler(cleanupHTTPClient, cfg.RecorderURL, producer, matchingLagChecker))
		mux.HandleFunc("GET /v1/admin/cleanup-unresolved-orders/status", cleanupAllStatusHandler())
		log.Printf("recorder 연동 활성화 (recorderUrl=%s) — 세션 종료 자동 정리 + 수동 정리 엔드포인트 사용 가능", cfg.RecorderURL)
	} else {
		log.Printf("RECORDER_URL이 없어 세션 종료 자동 정리 및 POST /v1/admin/cleanup-unresolved-orders 비활성화")
	}

	// POST /v1/admin/reset-matching-engine-book (2026-08-21, adminreset.go 참고) —
	// DB 정리(위)와는 별개로, 매칭엔진 자신의 Redis 전체 스냅샷 + 인메모리
	// 상태에만 남는 "잔량"을 지웁니다. rest.InClusterConfig()는 이 파드가 실제
	// K8s 클러스터 안에서(서비스어카운트 토큰 마운트된 채로) 돌 때만 성공합니다 —
	// 로컬 개발 환경에서는 실패하는 게 정상이라 라우트 자체를 등록하지 않고
	// 넘어갑니다(RECORDER_URL과 같은 선택적 기능 패턴).
	if k8sCfg, err := rest.InClusterConfig(); err != nil {
		log.Printf("K8s in-cluster config 획득 실패 — POST /v1/admin/reset-matching-engine-book 비활성화 (로컬 개발 환경이면 정상): %v", err)
	} else if clientset, err := kubernetes.NewForConfig(k8sCfg); err != nil {
		log.Printf("K8s 클라이언트 생성 실패 — POST /v1/admin/reset-matching-engine-book 비활성화: %v", err)
	} else {
		deployments := clientset.AppsV1().Deployments(matchingEngineNamespace)
		mux.HandleFunc("POST /v1/admin/reset-matching-engine-book", resetMatchingEngineBookHandler(redisClient, deployments, matchingEngineDeploymentName))
		log.Printf("K8s 연동 활성화 — POST /v1/admin/reset-matching-engine-book 사용 가능 (namespace=%s, deployment=%s)", matchingEngineNamespace, matchingEngineDeploymentName)
	}

	// ORDER_RECORDS_BUCKET이 비어있으면(로컬 개발 등) trader/replayengine과
	// 같은 기본값 규칙으로 로컬 ./orders 디렉터리를 읽습니다 — config.go 참고.
	var orderRecordsStorage orderrecords.Storage
	if cfg.OrderRecordsBucket != "" {
		orderRecordsStorage = orderrecords.NewS3Storage(cfg.OrderRecordsBucket)
	} else {
		orderRecordsStorage = orderrecords.NewLocalFileStorage("orders")
	}
	mux.HandleFunc("GET /v1/jobs/replay-preview", replayPreviewHandler(orderRecordsStorage))

	mux.Handle("GET /metrics", promhttp.Handler())

	// JOB_TRIGGER_QUEUE_URL이 없으면(로컬 개발 등) 이 라우트 자체를 등록하지
	// 않습니다 — config.go 참고, orderapi의 핵심 기능과는 무관한 선택 기능입니다.
	if cfg.JobTriggerQueueURL != "" {
		jobPublisher, err := jobtrigger.NewSQSPublisher(ctx, cfg.JobTriggerQueueURL)
		if err != nil {
			log.Fatalf("작업 트리거 SQS 발행자 생성 실패: %v", err)
		}
		mux.HandleFunc("POST /v1/jobs", startJobHandler(jobPublisher, cfg.OrderRecordsBucket))
		log.Printf("작업 트리거 활성화 (queue=%s)", cfg.JobTriggerQueueURL)
	} else {
		log.Printf("JOB_TRIGGER_QUEUE_URL이 없어 POST /v1/jobs 비활성화")
	}

	addr := ":" + cfg.Port
	log.Printf("주문 접수 API 서버 시작: %s (Kafka broker=%s, topic=%s, redis=%s)", addr, cfg.KafkaBroker, cfg.OrdersTopic, cfg.RedisAddr)
	log.Fatal(http.ListenAndServe(addr, mux))
}
