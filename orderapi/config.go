package main

import (
	"log"
	"os"

	"github.com/joho/godotenv"
)

// Config는 orderapi 실행에 필요한 환경변수를 담습니다.
type Config struct {
	Port               string
	KafkaBroker        string
	OrdersTopic        string
	ExecutionsTopic    string
	KafkaUseIAMAuth    bool
	RedisAddr          string
	RedisPassword      string
	RedisTLSEnabled    bool
	JobTriggerQueueURL string
	OrderRecordsBucket string
	RecorderURL        string
	PrometheusURL      string
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

	// JOB_TRIGGER_QUEUE_URL은 선택입니다 — 값이 있으면 POST /v1/jobs
	// (trader/replayengine 실행 요청을 SQS로 발행, infra/job-trigger.tf의
	// team1-sqs-job-trigger 참고)가 활성화되고, 없으면 main.go가 이 라우트
	// 자체를 등록하지 않습니다. KAFKA_BROKER/REDIS_ADDR과 달리 orderapi의
	// 핵심 기능(주문 접수/취소)은 이 값과 무관하게 동작하므로, 이 기능을 아직
	// 안 쓰는 로컬 개발 환경에 억지로 값을 채우게 하지 않습니다.
	jobTriggerQueueURL := os.Getenv("JOB_TRIGGER_QUEUE_URL")

	// ORDER_RECORDS_BUCKET도 선택입니다 — trader/replayengine의 -order-bucket
	// 플래그와 같은 기본값 규칙(비어있으면 로컬 ./orders 디렉터리)입니다.
	// GET /v1/jobs/replay-preview(2026-08-19 추가, "부하 시나리오 미리보기"
	// 지원)가 이 값으로 trader가 기록해둔 주문 파일을 읽습니다 — orderapi의
	// 핵심 기능(주문 접수/취소)과는 무관하므로 필수로 요구하지 않습니다.
	orderRecordsBucket := os.Getenv("ORDER_RECORDS_BUCKET")

	// RECORDER_URL도 선택입니다 — 값이 있으면 세션이 완전히 끝나는(그룹의
	// 마지막 멤버가 반납하는) 시점에 recorder의 GET /v1/orders/unresolved로
	// 그 세션이 남긴 미종결 주문을 물어봐서 취소합니다(2026-08-19, 부하테스트
	// 반복으로 매칭 엔진 인메모리 오더북에 미체결 주문이 계속 쌓여 OOMKilled까지
	// 간 사고 대응). 비어있으면 이 정리 로직을 건너뜁니다 — orderapi의 핵심
	// 기능과는 무관해서, 로컬 개발 환경에 recorder가 안 떠 있어도 orderapi는
	// 정상 동작해야 합니다.
	recorderURL := os.Getenv("RECORDER_URL")

	// PROMETHEUS_URL도 선택입니다 — 값이 있으면 GET /v1/metrics/cluster가
	// 활성 노드 수/백엔드 전체 파드 수/파드 재시작 누적/매칭엔진 호가창
	// 잔량/오토스케일링 현황을 그라파나 team1-overview 대시보드가 이미
	// 쓰고 있는 PromQL 그대로 모니터링 EC2의 Prometheus(infra/monitoring-ec2.tf,
	// 포트 9090)에 물어봅니다(2026-08-24, 사용자 제안 — 이 지표들을 orderapi가
	// 다시 만들 필요 없이 그라파나가 이미 검증해 쓰고 있는 값을 그대로
	// 재사용). RECORDER_URL과 같은 이유로 선택값 — 없으면 이 라우트를
	// 등록하지 않고, orderapi의 핵심 기능(주문 접수/취소)과는 무관합니다.
	prometheusURL := os.Getenv("PROMETHEUS_URL")

	return Config{
		Port:               port,
		KafkaBroker:        broker,
		OrdersTopic:        topic,
		ExecutionsTopic:    execTopic,
		KafkaUseIAMAuth:    kafkaUseIAMAuth,
		RedisAddr:          redisAddr,
		RedisPassword:      redisPassword,
		RedisTLSEnabled:    redisTLSEnabled,
		JobTriggerQueueURL: jobTriggerQueueURL,
		RecorderURL:        recorderURL,
		PrometheusURL:      prometheusURL,
		OrderRecordsBucket: orderRecordsBucket,
	}
}
