package main

import (
	"log"
	"os"

	"github.com/joho/godotenv"
)

// Config는 orderapi 실행에 필요한 환경변수를 담습니다.
type Config struct {
	Port            string
	KafkaBroker     string
	OrdersTopic     string
	ExecutionsTopic string
	KafkaUseIAMAuth bool
	RedisAddr       string
	RedisPassword   string
	RedisTLSEnabled bool
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

	// EXECUTIONS_TOPIC도 ORDERS_TOPIC과 같은 이유로 고정 관례값 기본값을
	// 둡니다 — order.Store가 체결을 반영하려고 구독하는 토픽(2026-08-10 추가).
	execTopic := os.Getenv("EXECUTIONS_TOPIC")
	if execTopic == "" {
		execTopic = "executions"
	}

	// 호가창 조회(GET /v1/markets/{market}/orderbook, FR-12)가 매칭 엔진이 써둔 Redis
	// 스냅샷을 읽으므로, KAFKA_BROKER와 같은 이유로 REDIS_ADDR도 필수로 요구합니다.
	redisAddr := os.Getenv("REDIS_ADDR")
	if redisAddr == "" {
		log.Fatal("REDIS_ADDR 환경변수가 필요합니다.")
	}

	// REDIS_PASSWORD는 선택입니다 — 로컬 dev-redis처럼 AUTH 없는 Redis는 비워두면
	// 되고, ElastiCache를 AUTH 토큰 있는 구성으로 만들면 채우면 됩니다(2026-08-10
	// 추가, docs/aws-infra-handoff.md 참고). 필수로 안 하는 이유는 REDIS_ADDR과
	// 달라서인데, "값이 없다"가 곧 "이 환경엔 AUTH가 없다"는 유효한 정상 상태라
	// KAFKA_BROKER 같은 필수값들과는 다릅니다.
	redisPassword := os.Getenv("REDIS_PASSWORD")

	// REDIS_TLS_ENABLED도 같은 이유로 선택(기본 false)입니다 — 2026-08-11 추가.
	// team1_truss ElastiCache는 transit_encryption_enabled=true인데 auth_token은
	// 안 둡니다(infra/elasticache.tf) — "TLS는 필수, 비밀번호는 없음"이라는
	// REDIS_PASSWORD 하나로는 표현 못 하는 조합이라 별도 플래그로 뽑았습니다.
	redisTLSEnabled := os.Getenv("REDIS_TLS_ENABLED") == "true"

	// KAFKA_USE_IAM_AUTH도 같은 이유로 선택(기본 false)입니다 — 2026-08-11
	// SCRAM에서 IAM으로 교체(CLAUDE.md 참고: MSK Serverless는 SASL/SCRAM을
	// 지원하지 않고 IAM 인증만 지원한다는 걸 AWS 공식 문서로 확인함). false면
	// 로컬 dev-kafka처럼 인증 없는 연결(kafkaclient.newDialer/newTransport가
	// nil을 돌려줌), true면 AWS_MSK_IAM(+TLS)으로 MSK에 인증합니다 — 자격증명은
	// AWS SDK v2 기본 체인(EC2 인스턴스 프로파일/EKS IRSA)을 그대로 씁니다.
	kafkaUseIAMAuth := os.Getenv("KAFKA_USE_IAM_AUTH") == "true"

	return Config{
		Port:            port,
		KafkaBroker:     broker,
		OrdersTopic:     topic,
		ExecutionsTopic: execTopic,
		KafkaUseIAMAuth: kafkaUseIAMAuth,
		RedisAddr:       redisAddr,
		RedisPassword:   redisPassword,
		RedisTLSEnabled: redisTLSEnabled,
	}
}
