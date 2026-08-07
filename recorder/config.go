package main

import (
	"log"
	"os"

	"github.com/joho/godotenv"
)

// Config는 기록기 실행에 필요한 환경변수를 담습니다.
type Config struct {
	KafkaBroker      string
	OrdersTopic      string
	ExecutionsTopic  string
	AssignmentsTopic string
	DatabaseURL      string
	ArchiveBucket    string
}

// LoadConfig는 로컬 .env 파일(있으면)을 읽어들인 뒤, 환경변수 기반 설정을
// 반환합니다. KAFKA_BROKER/DATABASE_URL은 기본값 없이 필수로 요구합니다 —
// orderapi/matching의 KAFKA_BROKER/REDIS_ADDR와 같은 이유(잘못된 값이 조용히
// localhost로 넘어가면 실제 연결 시도에서야 실패가 드러남). ARCHIVE_BUCKET이
// 비어있으면 로컬 ./records 디렉터리에 저장합니다(S3 버킷이 아직 없거나 로컬
// 개발 시).
func LoadConfig() Config {
	if err := godotenv.Load(); err != nil {
		log.Printf("'.env' 파일을 찾지 못했습니다 (prod 환경이라면 정상): %v", err)
	}

	broker := os.Getenv("KAFKA_BROKER")
	if broker == "" {
		log.Fatal("KAFKA_BROKER 환경변수가 필요합니다.")
	}

	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		log.Fatal("DATABASE_URL 환경변수가 필요합니다.")
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
		DatabaseURL:      dbURL,
		ArchiveBucket:    os.Getenv("ARCHIVE_BUCKET"),
	}
}
