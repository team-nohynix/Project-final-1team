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
	appsv1 "k8s.io/client-go/kubernetes/typed/apps/v1"
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
	matchingEngineNamespace      = "backend"
	matchingEngineDeploymentName = "matching-engine"
)

// sessionTTL은 세션 락의 Redis 만료 시간입니다 — 클라이언트(trader/replayengine)는
// 이 값의 1/3 주기로 하트비트를 보내야 하고(session.Client.Claim이 응답에 실어주는
// ttlSeconds를 그대로 씀), 크래시로 하트비트가 끊기면 이 시간 뒤 자동으로 풀립니다.
//
// **30초 → 120초 — 2026-08-27, 정상 완료된 리플레이가 "하트비트 없이 만료"로
// FAILED 처리되던 사고 대응.** replayengine은 하트비트와 주문 제출(마켓당
// 고루틴, 최대 20개 동시)이 같은 *http.Client(=같은 커넥션 풀)를 공유한다
// (submitter.go/main.go 참고). orderapi는 단일 인스턴스라, 가장 큰 마켓(실측
// XRP, 86,271건) 처리 구간처럼 대량 주문 제출이 몰리는 순간엔 하트비트
// PUT 요청도 같이 밀려서 30초 TTL을 넘겨 지연될 수 있다 — 클라이언트 쪽에서는
// "실패"가 아니라 그냥 "느리게 성공"이라 에러 로그도 안 남는데, Redis 쪽
// activeKey는 이미 만료된 뒤라 그 사이 들어온 GET /v1/sessions/last-run
// 폴링(대시보드든 운영 확인용이든) 하나가 하필 그 틈을 봐서 세션을 영구
// FAILED로 확정시켜버렸다(실측: 양쪽 샤드 모두 실제로는 정상 완료·반납
// 로그를 남겼는데도 최종 상태는 FAILED). 하트비트 주기는 이 값의 1/3이므로
// 120초로 늘리면 개별 하트비트가 40초까지 지연돼도 여유가 있다 — 크래시
// 감지가 최대 2분까지 늦어지는 트레이드오프는, 이 프로젝트의 부하테스트
// 특성(대량 동시 요청이 정상적인 트래픽 패턴) 앞에서는 충분히 감수할 만하다.
const sessionTTL = 120 * time.Second

// storeSweepInterval/storeSweepMaxAge — order.Store/idempotency.Store 주석 참고
// (2026-08-27 메모리 축출 사고 대응). maxAge 1시간은 정상적인 취소/멱등 재확인
// 시나리오보다 넉넉합니다.
const (
	storeSweepInterval = 10 * time.Minute
	storeSweepMaxAge   = 1 * time.Hour
)

func main() {
	cfg := LoadConfig()

	ctx := context.Background()

	store := order.NewStore()
	idem := idempotency.NewStore()
	// order.Store/idempotency.Store 둘 다 지금까지 지운 적이 없어 프로세스가
	// 오래 살아있고 대형 리플레이가 여러 번 돌면(오늘 실측) 무한정 자라 노드
	// 메모리 축출까지 갔습니다(order.go/idempotency.go 주석 참고, 2026-08-27).
	// 취소/멱등성 재확인은 실질적으로 최근 주문에서만 일어나므로 넉넉한
	// maxAge(1시간)로 주기적으로 정리합니다.
	go func() {
		ticker := time.NewTicker(storeSweepInterval)
		defer ticker.Stop()
		for range ticker.C {
			removedOrders := store.Sweep(storeSweepMaxAge)
			removedIdem := idem.Sweep(storeSweepMaxAge)
			if removedOrders > 0 || removedIdem > 0 {
				log.Printf("메모리 정리: 오래된 주문 %d건, 멱등성 캐시 %d건 제거", removedOrders, removedIdem)
			}
		}
	}()
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
		// Run이 에러로 끝나도(예: MSK가 idle 커넥션을 끊어서 오프셋 커밋이
		// "use of closed network connection"으로 실패하는 경우, 2026-08-27
		// 프로덕션에서 정확히 2시간마다 재현 확인됨) 이건 컨슈머 하나의
		// 일시적 네트워크 문제일 뿐입니다. 예전엔 여기서 log.Fatalf로 프로세스
		// 전체를 죽였는데, orderapi는 세션 상태를 인메모리로 들고 단일
		// 인스턴스로만 도는 구조라(위 주석 참고) 재시작되는 짧은 시간 동안
		// 주문 접수/취소/세션 조회까지 전부 503이 나는 진짜 장애로 번졌습니다.
		// FetchMessage/CommitMessages는 다음 호출에서 알아서 재연결하므로
		// Run을 다시 부르기만 하면 복구됩니다.
		for {
			err := execConsumer.Run(context.Background(), func(ctx context.Context, ev kafkaclient.ExecutionEvent) error {
				store.ApplyFill(ev.BuyOrderID, ev.Quantity)
				store.ApplyFill(ev.SellOrderID, ev.Quantity)
				return nil
			})
			log.Printf("executions 컨슈머 종료, 재연결 후 재시작: %v", err)
			time.Sleep(2 * time.Second)
		}
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
	mux.HandleFunc("GET /v1/sessions/previous-run-2", previousRun2Handler(sessionStore))
	mux.HandleFunc("GET /v1/sessions/runs", runHistoryHandler(sessionStore))
	mux.HandleFunc("GET /v1/dropped-orders", droppedOrdersHandler())

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

	// GET /v1/cluster-metrics (clustermetrics.go 참고) — PROMETHEUS_URL이
	// 없으면(로컬 개발 등) 등록하지 않습니다. RECORDER_URL과 같은 선택적
	// 기능 패턴 — orderapi의 핵심 기능과는 무관합니다.
	if cfg.PrometheusURL != "" {
		prom := newPromQuerier(cfg.PrometheusURL)
		mux.HandleFunc("GET /v1/cluster-metrics", clusterMetricsHandler(prom))
		log.Printf("Prometheus 연동 활성화 (prometheusUrl=%s) — GET /v1/cluster-metrics 사용 가능", cfg.PrometheusURL)
	} else {
		log.Printf("PROMETHEUS_URL이 없어 GET /v1/cluster-metrics 비활성화")
	}

	// POST /v1/admin/reset-matching-engine-book (2026-08-21, adminreset.go 참고) —
	// DB 정리(위)와는 별개로, 매칭엔진 자신의 Redis 전체 스냅샷 + 인메모리
	// 상태에만 남는 "잔량"을 지웁니다. rest.InClusterConfig()는 이 파드가 실제
	// K8s 클러스터 안에서(서비스어카운트 토큰 마운트된 채로) 돌 때만 성공합니다 —
	// 로컬 개발 환경에서는 실패하는 게 정상이라 라우트 자체를 등록하지 않고
	// 넘어갑니다(RECORDER_URL과 같은 선택적 기능 패턴).
	// deployments는 nil일 수 있습니다(로컬 개발 등 K8s in-cluster 접근이
	// 안 되는 환경) — reset-matching-engine-book뿐 아니라 아래
	// systemStatusHandler(파드 레플리카 수)도 이 값을 공유해서 씁니다.
	// DeploymentInterface는 네임스페이스 단위라 matching-engine 전용이
	// 아니라 backend 네임스페이스의 어떤 Deployment든(orderapi/recorder
	// 포함) 조회할 수 있습니다.
	var deployments appsv1.DeploymentInterface
	if k8sCfg, err := rest.InClusterConfig(); err != nil {
		log.Printf("K8s in-cluster config 획득 실패 — POST /v1/admin/reset-matching-engine-book 비활성화, 시스템 상태의 파드 수도 비활성화 (로컬 개발 환경이면 정상): %v", err)
	} else if clientset, err := kubernetes.NewForConfig(k8sCfg); err != nil {
		log.Printf("K8s 클라이언트 생성 실패 — POST /v1/admin/reset-matching-engine-book 비활성화: %v", err)
	} else {
		deployments = clientset.AppsV1().Deployments(matchingEngineNamespace)
		// force=true 워터마크 강제 설정(adminreset.go의 forceResetWatermarksToLatest)이
		// orders 토픽 파티션의 최신 오프셋을 직접 읽어야 해서, 프로듀서(producer)와는
		// 별개로 순수 조회용 Dialer가 필요합니다 — kafkaClient.NewDialer가 useIAM=false면
		// nil을 돌려주고, adminreset.go의 dialLeader가 그 nil을 인증 없는 기본 연결로
		// 처리합니다(로컬 dev-kafka 등).
		kafkaDialer, err := kafkaclient.NewDialer(ctx, cfg.KafkaUseIAMAuth)
		if err != nil {
			log.Fatalf("Kafka Dialer 생성 실패 (강제 초기화용): %v", err)
		}
		mux.HandleFunc("POST /v1/admin/reset-matching-engine-book", startResetMatchingEngineBookHandler(redisClient, deployments, matchingEngineDeploymentName, kafkaDialer, cfg.KafkaBroker, cfg.OrdersTopic))
		mux.HandleFunc("GET /v1/admin/reset-matching-engine-book/status", resetMatchingEngineBookStatusHandler())
		log.Printf("K8s 연동 활성화 — POST /v1/admin/reset-matching-engine-book 사용 가능 (namespace=%s, deployment=%s)", matchingEngineNamespace, matchingEngineDeploymentName)
	}

	// GET /v1/system-status(2026-08-24, systemstatus.go 참고) — 프론트
	// DashboardView "시스템 구성요소 상태" + "실시간 처리 흐름" 패널. 이
	// 조회 전용 Kafka Dialer는 위 reset-matching-engine-book의 것(K8s
	// in-cluster 접근이 성공해야만 만들어짐)과 달리 항상 만듭니다 — 상태
	// 패널 자체는 K8s 연동 여부와 무관하게 항상 떠 있어야 하는 기능이라서입니다.
	statusKafkaDialer, err := kafkaclient.NewDialer(ctx, cfg.KafkaUseIAMAuth)
	if err != nil {
		log.Fatalf("Kafka Dialer 생성 실패 (시스템 상태 확인용): %v", err)
	}
	mux.HandleFunc("GET /v1/system-status", systemStatusHandler(redisClient, statusKafkaDialer, cfg.KafkaBroker, cfg.OrdersTopic, cfg.RecorderURL, recorderHTTPClient, deployments))

	// ORDER_RECORDS_BUCKET이 비어있으면(로컬 개발 등) trader/replayengine과
	// 같은 기본값 규칙으로 로컬 ./orders 디렉터리를 읽습니다 — config.go 참고.
	var orderRecordsStorage orderrecords.Storage
	if cfg.OrderRecordsBucket != "" {
		orderRecordsStorage = orderrecords.NewS3Storage(cfg.OrderRecordsBucket)
	} else {
		orderRecordsStorage = orderrecords.NewLocalFileStorage("orders")
	}
	mux.HandleFunc("GET /v1/jobs/replay-preview", replayPreviewHandler(orderRecordsStorage))
	mux.HandleFunc("GET /v1/jobs/replay-dates", replayDatesHandler(orderRecordsStorage))

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
