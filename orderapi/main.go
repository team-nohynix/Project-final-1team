package main

import (
	"log"
	"net/http"
	"time"

	"github.com/redis/go-redis/v9"

	"orderapi/idempotency"
	"orderapi/kafkaclient"
	"orderapi/order"
	"orderapi/session"
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

	redisClient := redis.NewClient(&redis.Options{Addr: cfg.RedisAddr})
	defer redisClient.Close()

	sessionStore := session.NewRedisStore(redisClient, sessionTTL)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/orders", acceptOrderHandler(store, idem, producer))
	mux.HandleFunc("DELETE /v1/orders/{orderId}", cancelOrderHandler(store, producer))
	mux.HandleFunc("GET /v1/markets/{market}/orderbook", orderbookHandler(redisClient))
	mux.HandleFunc("POST /v1/sessions", claimSessionHandler(sessionStore))
	mux.HandleFunc("PUT /v1/sessions/{sessionId}/heartbeat", heartbeatSessionHandler(sessionStore))
	mux.HandleFunc("DELETE /v1/sessions/{sessionId}", releaseSessionHandler(sessionStore))

	addr := ":" + cfg.Port
	log.Printf("주문 접수 API 서버 시작: %s (Kafka broker=%s, topic=%s, redis=%s)", addr, cfg.KafkaBroker, cfg.OrdersTopic, cfg.RedisAddr)
	log.Fatal(http.ListenAndServe(addr, mux))
}
