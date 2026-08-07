package main

import (
	"log"
	"os"

	"github.com/joho/godotenv"
)

// Config는 매칭 엔진 실행에 필요한 환경변수를 담습니다.
type Config struct {
	KafkaBroker      string
	OrdersTopic      string
	ExecutionsTopic  string
	AssignmentsTopic string
	RedisAddr        string
}

// LoadConfig는 로컬의 .env 파일(있으면)을 읽어들인 뒤, 환경변수 기반 설정을 반환합니다.
// KAFKA_BROKER/REDIS_ADDR는 기본값을 두지 않고 필수로 요구합니다 — 환경마다 다른 값인데
// (로컬은 localhost, 실제 배포 환경은 MSK/ElastiCache 엔드포인트) 없을 때 조용히 localhost로
// 넘어가면 배포 환경에서는 그 자리에서 에러가 나는 게 아니라 한참 뒤 연결 시도에서야 실패가
// 드러납니다 — orderapi/config.go의 KAFKA_BROKER, trader/config.go의 BACKEND_URL과 같은 이유.
// ORDERS_TOPIC/EXECUTIONS_TOPIC은 토픽 이름이 환경과 무관하게 고정된 값이라 기본값을 둡니다.
func LoadConfig() Config {
	if err := godotenv.Load(); err != nil {
		log.Printf("'.env' 파일을 찾지 못했습니다 (prod 환경이라면 정상): %v", err)
	}

	broker := os.Getenv("KAFKA_BROKER")
	if broker == "" {
		log.Fatal("KAFKA_BROKER 환경변수가 필요합니다.")
	}

	redisAddr := os.Getenv("REDIS_ADDR")
	if redisAddr == "" {
		log.Fatal("REDIS_ADDR 환경변수가 필요합니다.")
	}

	ordersTopic := os.Getenv("ORDERS_TOPIC")
	if ordersTopic == "" {
		ordersTopic = "orders"
	}

	executionsTopic := os.Getenv("EXECUTIONS_TOPIC")
	if executionsTopic == "" {
		executionsTopic = "executions"
	}

	assignmentsTopic := os.Getenv("ASSIGNMENTS_TOPIC")
	if assignmentsTopic == "" {
		assignmentsTopic = "assignments"
	}

	return Config{
		KafkaBroker:      broker,
		OrdersTopic:      ordersTopic,
		ExecutionsTopic:  executionsTopic,
		AssignmentsTopic: assignmentsTopic,
		RedisAddr:        redisAddr,
	}
}
