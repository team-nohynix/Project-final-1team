package main

import (
	"context"
	"log"
	"net/http"
	"time"

	"github.com/redis/go-redis/v9"

	"orderapi/backpressure"
	"orderapi/idempotency"
	"orderapi/kafkaclient"
	"orderapi/order"
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
)

// sessionTTL은 세션 락의 Redis 만료 시간입니다 — 클라이언트(trader/replayengine)는
// 이 값의 1/3 주기로 하트비트를 보내야 하고(session.Client.Claim이 응답에 실어주는
// ttlSeconds를 그대로 씀), 크래시로 하트비트가 끊기면 이 시간 뒤 자동으로 풀립니다.
const sessionTTL = 30 * time.Second

func main() {
	cfg := LoadConfig()

	store := order.NewStore()
	idem := idempotency.NewStore()
	producer := kafkaclient.NewOrderProducer(cfg.KafkaBroker, cfg.OrdersTopic)
	defer producer.Close()

	// executions를 구독해 order.Store가 체결을 반영합니다(2026-08-10) —
	// 이게 없으면 cancelOrderHandler의 ORDER_ALREADY_FILLED 체크가 데이터
	// 소스가 없어 무의미했습니다(CLAUDE.md 참고). matching/recorder처럼 별도
	// 컨슈머 그룹("orderapi-executions")을 씁니다. BuyOrderID/SellOrderID
	// 둘 다 시도합니다 — 이 orderapi 인스턴스가 접수한 쪽만 Store에 있으므로
	// (반대편은 이미 orderapi가 재시작됐거나 원래 이 인스턴스가 접수한 게
	// 아닐 수 있음) 한쪽만 찾아지는 것도 정상입니다.
	execConsumer := kafkaclient.NewExecutionConsumer(cfg.KafkaBroker, cfg.ExecutionsTopic, "orderapi-executions")
	defer execConsumer.Close()
	go func() {
		err := execConsumer.Run(context.Background(), func(ctx context.Context, ev kafkaclient.ExecutionEvent) error {
			store.ApplyFill(ev.BuyOrderID, ev.Quantity)
			store.ApplyFill(ev.SellOrderID, ev.Quantity)
			return nil
		})
		log.Fatalf("executions 컨슈머 종료: %v", err)
	}()

	redisClient := redis.NewClient(&redis.Options{Addr: cfg.RedisAddr})
	defer redisClient.Close()

	sessionStore := session.NewRedisStore(redisClient, sessionTTL)
	lagChecker := backpressure.MultiChecker{
		&backpressure.RedisChecker{Client: redisClient, Key: backpressureRecorderLagKey},
		&backpressure.RedisChecker{Client: redisClient, Key: backpressureMatchingLagKey},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/orders", acceptOrderHandler(store, idem, producer, lagChecker))
	mux.HandleFunc("DELETE /v1/orders/{orderId}", cancelOrderHandler(store, producer))
	mux.HandleFunc("GET /v1/markets/{market}/orderbook", orderbookHandler(redisClient))
	mux.HandleFunc("POST /v1/sessions", claimSessionHandler(sessionStore))
	mux.HandleFunc("PUT /v1/sessions/{sessionId}/heartbeat", heartbeatSessionHandler(sessionStore))
	mux.HandleFunc("DELETE /v1/sessions/{sessionId}", releaseSessionHandler(sessionStore))

	addr := ":" + cfg.Port
	log.Printf("주문 접수 API 서버 시작: %s (Kafka broker=%s, topic=%s, redis=%s)", addr, cfg.KafkaBroker, cfg.OrdersTopic, cfg.RedisAddr)
	log.Fatal(http.ListenAndServe(addr, mux))
}
