package main

import (
	"log"
	"os"

	"github.com/joho/godotenv"
)

// Config는 orderapi 실행에 필요한 환경변수를 담습니다.
type Config struct {
	Port        string
	KafkaBroker string
	OrdersTopic string
	RedisAddr   string
}

// LoadConfig는 로컬의 .env 파일(있으면)을 읽어들인 뒤, 환경변수 기반 설정을 반환합니다.
// backend/config.Load()와 같은 패턴 — prod에는 .env가 없고 환경변수가 직접 주입되므로
// .env 로드 실패 자체는 오류로 취급하지 않습니다.
//
// KAFKA_BROKER는 기본값을 두지 않고 필수로 요구합니다 — 이 값은 환경마다 다르고
// (로컬은 localhost:9092, 실제 배포 환경은 MSK 등 별도 엔드포인트), 없을 때 조용히
// "localhost:9092"로 넘어가면 실제 배포 환경에서는 그 자리에서 에러가 나는 게 아니라
// 한참 뒤 Kafka 접속 시도에서야 실패가 드러납니다 — 잘못된 설정은 시작 시점에 바로
// 드러나야 합니다(backend/config.go의 S3_BUCKET과 같은 이유). PORT/ORDERS_TOPIC은
// 환경이 달라져도 웬만하면 같은 값이고 틀려도 바로 눈에 보이는 실패로 이어지므로
// 기본값을 유지합니다.
func LoadConfig() Config {
	if err := godotenv.Load(); err != nil {
		log.Printf("'.env' 파일을 찾지 못했습니다 (prod 환경이라면 정상): %v", err)
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8081"
	}

	broker := os.Getenv("KAFKA_BROKER")
	if broker == "" {
		log.Fatal("KAFKA_BROKER 환경변수가 필요합니다.")
	}

	topic := os.Getenv("ORDERS_TOPIC")
	if topic == "" {
		topic = "orders"
	}

	// 호가창 조회(GET /v1/markets/{market}/orderbook, FR-12)가 매칭 엔진이 써둔 Redis
	// 스냅샷을 읽으므로, KAFKA_BROKER와 같은 이유로 REDIS_ADDR도 필수로 요구합니다.
	redisAddr := os.Getenv("REDIS_ADDR")
	if redisAddr == "" {
		log.Fatal("REDIS_ADDR 환경변수가 필요합니다.")
	}

	return Config{Port: port, KafkaBroker: broker, OrdersTopic: topic, RedisAddr: redisAddr}
}
