package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"

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

	store := snapshotstore.NewRedisStore(cfg.RedisAddr)
	producer := kafkaclient.NewExecutionProducer(cfg.KafkaBroker, cfg.ExecutionsTopic)
	defer producer.Close()
	assignments := kafkaclient.NewAssignmentProducer(cfg.KafkaBroker, cfg.AssignmentsTopic)
	defer assignments.Close()
	instanceID := newInstanceID()

	redisClient := redis.NewClient(&redis.Options{Addr: cfg.RedisAddr})
	tracker := rebalance.NewLoadTracker(redisClient, cfg.KafkaBroker, cfg.OrdersTopic, TargetMarkets)
	balancer := rebalance.NewLoadAwareBalancer(tracker, TargetMarkets)

	registry := newMarketRegistry(producer, store, assignments, instanceID)

	consumer, err := kafkaclient.NewGroupConsumer(cfg.KafkaBroker, consumerGroupID, cfg.OrdersTopic, balancer, TargetMarkets, registry)
	if err != nil {
		log.Fatalf("컨슈머 그룹 생성 실패: %v", err)
	}
	defer consumer.Close()

	log.Printf("매칭 엔진 시작 (instanceId=%s, Kafka broker=%s, orders=%s, executions=%s, assignments=%s, redis=%s, 마켓 %d개, group=%s)",
		instanceID, cfg.KafkaBroker, cfg.OrdersTopic, cfg.ExecutionsTopic, cfg.AssignmentsTopic, cfg.RedisAddr, len(TargetMarkets), consumerGroupID)

	if err := consumer.Run(ctx); err != nil {
		log.Fatalf("매칭 엔진 종료: %v", err)
	}
}
