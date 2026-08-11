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
type marketRegistry struct {
	mu               sync.Mutex
	engines          map[string]*engine.Engine
	producer         engine.ExecutionPublisher
	store            engine.SnapshotStore
	snapshotEvery    int
	snapshotInterval time.Duration

	// assignments/instanceID는 FR-11 배정/반납을 감사 목적으로 기록하기 위한
	// 것뿐입니다 — 매칭 엔진 자신의 정합성은 이미 store(Redis 스냅샷+Handoff)가
	// 보장하므로, 이 발행이 실패해도 Acquire/Release 자체를 막지 않습니다.
	assignments *kafkaclient.AssignmentProducer
	instanceID  string
}

func newMarketRegistry(producer engine.ExecutionPublisher, store engine.SnapshotStore, assignments *kafkaclient.AssignmentProducer, instanceID string) *marketRegistry {
	return &marketRegistry{
		engines:          make(map[string]*engine.Engine),
		producer:         producer,
		store:            store,
		snapshotEvery:    snapshotEveryEvents,
		snapshotInterval: snapshotInterval,
		assignments:      assignments,
		instanceID:       instanceID,
	}
}

func (r *marketRegistry) Acquire(ctx context.Context, market string) (int64, error) {
	e := engine.New(market, r.producer, r.store, r.snapshotEvery, r.snapshotInterval)
	resumeFrom, err := e.Recover(ctx)
	if err != nil {
		return 0, fmt.Errorf("복구 실패: %w", err)
	}

	r.mu.Lock()
	r.engines[market] = e
	r.mu.Unlock()

	if err := r.assignments.PublishAssigned(ctx, market, r.instanceID); err != nil {
		log.Printf("배정 기록 이벤트 발행 실패 (market=%s): %v", market, err)
	}
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
		return err
	}
	if err := r.assignments.PublishReleased(ctx, market, r.instanceID); err != nil {
		log.Printf("반납 기록 이벤트 발행 실패 (market=%s): %v", market, err)
	}
	return nil
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

	registry := newMarketRegistry(producer, store, assignments, instanceID)

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
		Flag:          &backpressure.RedisFlag{Client: redisClient, Key: backpressureRedisKey},
		HighWatermark: backpressureHighWatermark,
		LowWatermark:  backpressureLowWatermark,
		CheckInterval: backpressureCheckInterval,
	}
	go matchingWatcher.Run(ctx)

	log.Printf("매칭 엔진 시작 (instanceId=%s, Kafka broker=%s, orders=%s, executions=%s, assignments=%s, redis=%s, 마켓 %d개, group=%s)",
		instanceID, cfg.KafkaBroker, cfg.OrdersTopic, cfg.ExecutionsTopic, cfg.AssignmentsTopic, cfg.RedisAddr, len(TargetMarkets), consumerGroupID)

	if err := consumer.Run(ctx); err != nil {
		log.Fatalf("매칭 엔진 종료: %v", err)
	}
}
