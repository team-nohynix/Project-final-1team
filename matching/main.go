package main

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"

	"matching/backpressure"
	"matching/engine"
	"matching/kafkaclient"
	"matching/rebalance"
	"matching/snapshotstore"
)

// 스냅샷은 마켓당 이 이벤트 수 또는 이 시간 중 먼저 도달하는 쪽마다 한 번씩 (비동기로)
// Redis에 남깁니다 — FR-08(재시작 복구) 체크포인트와 FR-12(호가창 조회) 응답을 겸합니다.
// (마켓을 다른 인스턴스에 넘겨줄 때는 이 비동기 주기와 무관하게 항상 동기 Handoff를 씁니다.)
const (
	snapshotEveryEvents = 50
	snapshotInterval    = 100 * time.Millisecond

	// consumerGroupID는 이 매칭 엔진 배포 전체가 공유하는 고정값입니다(FR-11) — 인스턴스마다
	// 다르면 각자 자기 혼자만의 그룹에 들어가서 재분배 자체가 일어나지 않습니다. 토픽 이름
	// 기본값(config.go의 ORDERS_TOPIC)과 같은 이유로 환경변수가 아니라 코드 상수입니다.
	consumerGroupID = "matching-engine"

	// matching 자체 컨슈머 랙 기반 백프레셔(NFR-13, 원래 matching 쪽으로 스코프된
	// 버전 — recorder 랙 버전과는 별개, matching/backpressure 패키지 설명 참고).
	// Redis 키는 recorder의 backpressureRedisKey("backpressure:recorder_lag")와
	// 겹치지 않도록 별도로 둡니다 — orderapi가 두 키를 각각 독립적으로 확인합니다.
	//
	// 2026-08-11 FR-19 실측(real trader→replayengine 재생, 20개 마켓, 4번의
	// 부하 시나리오 — 1/2/4/7 replayengine 인스턴스)에서 matching의 이 랙
	// 플래그는 단 한 번도 켜지지 않았습니다 — 즉 이 값들은 실제로 겪어본
	// 부하 범위 안에서는 문제없이 여유가 있다는 게 확인됐습니다(같은 테스트에서
	// recorder는 데드락 버그가 드러났고 watermark를 낮췄음 — CLAUDE.md의
	// "RDS admission control via recorder consumer lag" 참고). 그래서 여기는
	// 낮추지 않고 그대로 둡니다 — 다만 이 테스트가 matching의 진짜 한계치를
	// 찾아낸 건 아니라서(여유가 있다는 것만 확인됨, 얼마나 여유 있는지는
	// 모름) 여전히 잠정값입니다.
	backpressureHighWatermark = 5000
	backpressureLowWatermark  = 1000
	backpressureCheckInterval = 5 * time.Second
	backpressureRedisKey      = "backpressure:matching_lag"
)

// marketRegistry는 kafkaclient.MarketLifecycle을 구현해 마켓별 Engine의 생명주기를
// 관리합니다. 파티션을 새로 배정받으면(Acquire) 항상 새 Engine을 만들어 Redis 스냅샷에서
// 복구하고, 반납할 때(Release)는 Handoff로 확정 저장한 뒤 맵에서 제거합니다 — 오래된
// 인메모리 상태를 재사용하지 않는 것이 핵심입니다(다시 배정받으면 그 시점의 스냅샷에서
// 다시 복구). 여러 파티션(마켓)의 고루틴이 동시에 이 맵을 건드릴 수 있어 mu로 보호하지만,
// 같은 마켓의 Apply는 항상 그 마켓을 담당하는 고루틴 하나에서만 순차 호출되므로
// engine.Engine 자체의 동시성 무보호 전제는 그대로 유지됩니다.
//
// **marketLock — 2026-08-27, 정합성 검사에서 실제로 잡힌 "중복 체결"(한 주문의
// 체결 수량 합이 원 수량을 초과) 사고 대응.** gen.Start의 계약(consumer.go 주석
// 참고)은 "이 인스턴스 자신의 재가입은 자신의 Release/Handoff가 끝나야 일어난다"만
// 보장합니다 — 리밸런스 코디네이터가 그 재가입(JoinGroup)을 기다려줄지는 Kafka의
// rebalance timeout(kafka-go 기본값, 이 코드베이스는 명시적으로 설정 안 함) 안에
// 이 인스턴스가 Handoff를 끝내고 다시 합류하느냐에 달려 있습니다. 오늘 이 마켓이
// 0→10개로 급격히 스케일아웃하며 리밸런스가 연달아 일어난 구간에서, 어느 인스턴스의
// Handoff(Redis 왕복)가 그 타임아웃을 넘기면 코디네이터가 그 인스턴스를 기다리지
// 않고 다음 세대를 확정할 수 있고, 그러면 새 담당자가 옛 담당자의 마지막 체결이
// 반영되기 전 스냅샷으로 Recover해 같은 주문을 다시 매칭 — 실측 44건의 "중복 체결"과
// 시점이 정확히 일치합니다. Kafka의 리밸런스 타이밍에 기대는 대신, 마켓 소유권
// 자체를 Redis 락으로 명시적으로 직렬화합니다 — Acquire는 락을 얻어야 스냅샷을
// 읽고, Release는 Handoff까지 끝난 뒤에만 락을 놓으므로, 코디네이터가 무엇을
// 하든 두 인스턴스가 동시에 같은 마켓을 매칭할 수 없습니다.
const (
	marketLockTTL        = 15 * time.Second
	marketLockRetryDelay = 100 * time.Millisecond
	marketLockMaxWait    = 10 * time.Second
)

type marketRegistry struct {
	mu               sync.Mutex
	engines          map[string]*engine.Engine
	producer         engine.ExecutionPublisher
	store            engine.SnapshotStore
	redisClient      *redis.Client
	snapshotEvery    int
	snapshotInterval time.Duration

	// assignments/instanceID는 FR-11 배정/반납을 감사 목적으로 기록하기 위한
	// 것뿐입니다 — 매칭 엔진 자신의 정합성은 이미 store(Redis 스냅샷+Handoff)가
	// 보장하므로, 이 발행이 실패해도 Acquire/Release 자체를 막지 않습니다.
	assignments *kafkaclient.AssignmentProducer
	instanceID  string
}

func newMarketRegistry(producer engine.ExecutionPublisher, store engine.SnapshotStore, redisClient *redis.Client, assignments *kafkaclient.AssignmentProducer, instanceID string) *marketRegistry {
	return &marketRegistry{
		engines:          make(map[string]*engine.Engine),
		producer:         producer,
		store:            store,
		redisClient:      redisClient,
		snapshotEvery:    snapshotEveryEvents,
		snapshotInterval: snapshotInterval,
		assignments:      assignments,
		instanceID:       instanceID,
	}
}

func marketLockKey(market string) string {
	return "matching:market-lock:" + market
}

// releaseLockScript는 "내가 아직 이 락의 주인일 때만 지운다"를 원자적으로 합니다 —
// TTL이 만료돼 다른 인스턴스가 이미 새로 잡은 락을 실수로 지우면 안 되므로 GET+DEL을
// Lua로 묶습니다(표준 Redis 분산 락 해제 패턴).
var releaseLockScript = redis.NewScript(`
	if redis.call("GET", KEYS[1]) == ARGV[1] then
		return redis.call("DEL", KEYS[1])
	end
	return 0
`)

// acquireMarketLock은 이 마켓의 소유권을 Redis 락으로 직렬화합니다 — 이전 담당자가
// 아직 락을 들고 있으면(Handoff 진행 중이거나, 리밸런스 타임아웃을 넘겨 코디네이터는
// 이미 다음 세대로 넘어갔지만 실제 정리는 아직 안 끝난 경우) 짧게 재시도하며
// 기다립니다. marketLockMaxWait를 넘기면 포기하고 에러를 돌려주는데, 호출부
// (consumePartition)는 이미 "이번 세대엔 이 마켓을 못 맡는다"를 로그만 남기고
// 넘어가는 안전한 경로를 갖고 있습니다(다음 리밸런스에서 다시 시도됨) — 무한정
// 기다리다 이 goroutine이 멈춰있는 것보다 낫습니다.
func (r *marketRegistry) acquireMarketLock(ctx context.Context, market string) error {
	deadline := time.Now().Add(marketLockMaxWait)
	for {
		ok, err := r.redisClient.SetNX(ctx, marketLockKey(market), r.instanceID, marketLockTTL).Result()
		if err != nil {
			return fmt.Errorf("마켓 락 획득 실패 (market=%s): %w", market, err)
		}
		if ok {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("마켓 락 획득 타임아웃 (market=%s, %v 대기)", market, marketLockMaxWait)
		}
		select {
		case <-time.After(marketLockRetryDelay):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (r *marketRegistry) releaseMarketLock(ctx context.Context, market string) {
	if err := releaseLockScript.Run(ctx, r.redisClient, []string{marketLockKey(market)}, r.instanceID).Err(); err != nil && err != redis.Nil {
		log.Printf("마켓 락 해제 실패 (market=%s): %v — TTL(%v) 후 자동 만료됨", market, err, marketLockTTL)
	}
}

func (r *marketRegistry) Acquire(ctx context.Context, market string) (int64, error) {
	if err := r.acquireMarketLock(ctx, market); err != nil {
		return 0, err
	}
	e := engine.New(market, r.producer, r.store, r.snapshotEvery, r.snapshotInterval)
	resumeFrom, err := e.Recover(ctx)
	if err != nil {
		r.releaseMarketLock(ctx, market)
		return 0, fmt.Errorf("복구 실패: %w", err)
	}

	r.mu.Lock()
	r.engines[market] = e
	r.mu.Unlock()

	if err := r.assignments.PublishAssigned(ctx, market, r.instanceID); err != nil {
		log.Printf("배정 기록 이벤트 발행 실패 (market=%s): %v", market, err)
	}
	marketAcquiredCounter.Add(1)
	return resumeFrom, nil
}

func (r *marketRegistry) Apply(ctx context.Context, market string, ev engine.OrderEvent) error {
	r.mu.Lock()
	e, ok := r.engines[market]
	r.mu.Unlock()
	if !ok {
		return fmt.Errorf("담당하지 않는 마켓의 이벤트가 도착함 (market=%s)", market)
	}
	return e.Apply(ctx, ev)
}

func (r *marketRegistry) Release(ctx context.Context, market string) error {
	r.mu.Lock()
	e, ok := r.engines[market]
	delete(r.engines, market)
	r.mu.Unlock()
	if !ok {
		return nil
	}
	if err := e.Handoff(ctx); err != nil {
		// 락은 일부러 안 놓습니다 — Handoff가 실패했다는 건 확정 저장이 안 됐다는
		// 뜻이라, 지금 놓으면 다음 인수자가 이 마켓의 마지막 체결이 반영 안 된
		// 스냅샷으로 Recover해 바로 이 기능이 막으려는 그 사고(중복 체결)가
		// 재현됩니다. TTL(marketLockTTL)이 지나면 자동으로 풀립니다.
		return err
	}
	r.releaseMarketLock(ctx, market)
	if err := r.assignments.PublishReleased(ctx, market, r.instanceID); err != nil {
		log.Printf("반납 기록 이벤트 발행 실패 (market=%s): %v", market, err)
	}
	marketReleasedCounter.Add(1)
	return nil
}

// TotalBookSize는 이 인스턴스가 지금 담당 중인 모든 마켓의 호가창 미체결 주문 개수
// 합계입니다(matching_engine_book_size 메트릭용, 2026-08-21 추가) — consumer.Lag와
// 같은 이유로 스크레이프마다 그대로 계산합니다: 맵 길이 합산이라 가볍고, 컨슈머 랙과
// 달리 "이미 다 읽었지만 체결 상대가 없어 메모리에 쌓인 양"을 재기 때문에 랙이 0이어도
// 이 값만 계속 자랄 수 있습니다(KEDA가 이 두 지표를 별도 트리거로 같이 봅니다).
func (r *marketRegistry) TotalBookSize() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	total := 0
	for _, e := range r.engines {
		total += e.BookSize()
	}
	return total
}

// newInstanceID는 이 매칭 엔진 프로세스를 식별할 값을 만듭니다 — assignments
// 이벤트에 "누가 이 마켓을 맡았는지"를 남기는 용도뿐이라, orderapi/session의
// newSessionID와 같은 crypto/rand 기반이면 충분합니다(순번 카운터가 아닌 이유도
// 같음 — 여러 인스턴스가 동시에 존재).
func newInstanceID() string {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("engine_%d", time.Now().UnixNano())
	}
	return "engine_" + hex.EncodeToString(buf)
}

func main() {
	cfg := LoadConfig()
	ctx := context.Background()

	// matching 안의 모든 Redis 사용(스냅샷 저장, 부하 추적, 백프레셔 플래그)이
	// 이 클라이언트 하나를 공유합니다 — REDIS_PASSWORD/REDIS_TLS_ENABLED
	// 설정을 반영하는 곳이 여기 한 군데뿐이어야, snapshotstore가 별도로
	// 인증 없는 클라이언트를 몰래 만드는 것과 같은 버그를 구조적으로 막습니다.
	redisOpts := &redis.Options{Addr: cfg.RedisAddr, Password: cfg.RedisPassword}
	if cfg.RedisTLSEnabled {
		redisOpts.TLSConfig = &tls.Config{}
	}
	redisClient := redis.NewClient(redisOpts)

	store := snapshotstore.NewRedisStore(redisClient)
	producer, err := kafkaclient.NewExecutionProducer(ctx, cfg.KafkaBroker, cfg.ExecutionsTopic, cfg.KafkaUseIAMAuth)
	if err != nil {
		log.Fatalf("체결 프로듀서 생성 실패: %v", err)
	}
	defer producer.Close()
	assignments, err := kafkaclient.NewAssignmentProducer(ctx, cfg.KafkaBroker, cfg.AssignmentsTopic, cfg.KafkaUseIAMAuth)
	if err != nil {
		log.Fatalf("배정 이벤트 프로듀서 생성 실패: %v", err)
	}
	defer assignments.Close()
	instanceID := newInstanceID()

	tracker, err := rebalance.NewLoadTracker(ctx, redisClient, cfg.KafkaBroker, cfg.OrdersTopic, TargetMarkets, cfg.KafkaUseIAMAuth)
	if err != nil {
		log.Fatalf("부하 추적기 생성 실패: %v", err)
	}
	balancer := rebalance.NewLoadAwareBalancer(tracker, TargetMarkets)

	registry := newMarketRegistry(producer, store, redisClient, assignments, instanceID)

	consumer, err := kafkaclient.NewGroupConsumer(ctx, cfg.KafkaBroker, consumerGroupID, cfg.OrdersTopic, balancer, TargetMarkets, registry, cfg.KafkaUseIAMAuth)
	if err != nil {
		log.Fatalf("컨슈머 그룹 생성 실패: %v", err)
	}
	defer consumer.Close()

	// matching 자체 랙 감시: 이 인스턴스가 지금 담당 중인 파티션들의 랙 합계(consumer.Lag,
	// GroupConsumer.readers 레지스트리 기반)를 주기적으로 확인해 Redis 플래그를 켭니다.
	// 비활성일 때는 명시적으로 끄지 않습니다(matching/backpressure.RedisFlag 참고) — FR-11로
	// 여러 인스턴스가 동시에 도는데, 한 인스턴스가 회복됐다고 다른 인스턴스가 세운 플래그를
	// 지워버리면 안 되기 때문입니다. orderapi는 이 키와 recorder의 키를 둘 다 확인합니다.
	matchingWatcher := &backpressure.Watcher{
		Sources:       []backpressure.LagSource{consumer.Lag},
		Flag:          instrumentedFlag{inner: &backpressure.RedisFlag{Client: redisClient, Key: backpressureRedisKey}},
		HighWatermark: backpressureHighWatermark,
		LowWatermark:  backpressureLowWatermark,
		CheckInterval: backpressureCheckInterval,
	}
	go matchingWatcher.Run(ctx)

	startMetricsServer(cfg.MetricsPort, consumer.Lag, registry.TotalBookSize)

	log.Printf("매칭 엔진 시작 (instanceId=%s, Kafka broker=%s, orders=%s, executions=%s, assignments=%s, redis=%s, 마켓 %d개, group=%s)",
		instanceID, cfg.KafkaBroker, cfg.OrdersTopic, cfg.ExecutionsTopic, cfg.AssignmentsTopic, cfg.RedisAddr, len(TargetMarkets), consumerGroupID)

	if err := consumer.Run(ctx); err != nil {
		log.Fatalf("매칭 엔진 종료: %v", err)
	}
}
