# 배포 환경변수 / CLI 플래그 정리

## 관련 문서
- [CLAUDE.md](../CLAUDE.md) — 각 모듈의 Commands/Architecture 섹션에 있는 원본 설명
- [truss-architecture.html](truss-architecture.html) — 이 값들이 실제로 어디(MSK/RDS/ElastiCache 등)를 가리켜야 하는지, 전체 아키텍처

이 문서는 **각 모듈의 `config.go`/`main.go`를 직접 읽고 정리한 것**이라 CLAUDE.md의 서술형 설명보다 이 값들 자체를 빠르게 확인하고 싶을 때 쓰는 표 위주 참조 문서다. 로컬 개발용 `.env` 값(예: `localhost:9092`)이 아니라 **AWS에 배포할 때 채워야 하는 실제 값의 형태**를 기준으로 적었다.

## 실제 인프라 현황

`terraform state list`/`aws` CLI로 직접 확인한 값이다 — 아래 표들의 "AWS에서 채울 값"이 실제로 이 값이라고 봐도 된다.

| 리소스 | 실제 값 |
|---|---|
| MSK 브로커(IAM) | `boot-naw3iax2.c1.kafka-serverless.ap-northeast-2.amazonaws.com:9098` |
| DB 엔진 | MySQL 8.4 (Docker, EC2 `team1-mysql`/`i-09f1dca7e19bbd3ff`, private IP `10.10.10.178`) |
| DB 자격증명 | K8s Secret `recorder-db-secret`(`DATABASE_URL` 키, 완성된 DSN) — Terraform이 관리(mysql-ec2.tf) |
| S3 `team1-truss-market-data` | 생성+Terraform 반영 완료 |
| S3 `team1-truss-order-records` | 생성+Terraform 반영 완료(trader가 쓸 버킷) |
| S3 `team1-truss-trade-results` | 생성+Terraform 반영 완료 |
| ECR | `727646470302.dkr.ecr.ap-northeast-2.amazonaws.com/team1-truss`, 태그 `{모듈}-latest` — 6개 모듈 전부 푸시 완료(아래 "컨테이너 이미지" 참고) |
| EKS | `team1-eks`, 네임스페이스 `backend`(orderapi/matching/recorder), `collector`, `ai-trader`, `replay` — 서비스 어카운트/IRSA 전부 연결 완료 |

---

## 1. `backend/` (시세 수집기)

| 이름 | 필수 여부 | 로컬 기본값 | AWS에서 채울 값 |
|---|---|---|---|
| `APP_ENV` | **필수** (기본값 없음, 값이 `dev`/`prod`가 아니면 즉시 종료) | — | `prod` |
| `PORT` | 선택 | `8080` | 보통 그대로 둠 |
| `S3_BUCKET` | `APP_ENV=prod`일 때만 필수 | — | `team1-truss-market-data` |

CLI 플래그 없음 — `go run .`으로 HTTP 서버만 띄우고, `POST /v1/collect`가 실제 수집 트리거.

---

## 2. `orderapi/` (주문 접수 API, role A)

| 이름 | 필수 여부 | 로컬 기본값 | AWS에서 채울 값 |
|---|---|---|---|
| `KAFKA_BROKER` | **필수** | — | MSK(또는 자체 Kafka) 브로커 엔드포인트 |
| `REDIS_ADDR` | **필수** | — | ElastiCache(또는 자체 Redis) 엔드포인트 |
| `PORT` | 선택 | `8081` | 보통 그대로 둠 |
| `ORDERS_TOPIC` | 선택 | `orders` | 그대로 둠(고정 관례값) |
| `EXECUTIONS_TOPIC` | 선택 | `executions` | 그대로 둠(고정 관례값) |
| `KAFKA_USE_IAM_AUTH` | 선택 | `false` | `true`면 AWS_MSK_IAM으로 MSK에 인증(자격증명은 SDK 기본 체인/EKS IRSA) — 6번 문서 "Kafka/Redis 인증" 참고. `false`면 인증 없이 붙음(로컬 그대로) |
| `REDIS_PASSWORD` | 선택 | 비움 | ElastiCache AUTH 토큰. 비우면 인증 없이 붙음 |
| `REDIS_TLS_ENABLED` | 선택 | `false` | `true`면 Redis 연결에 TLS 사용 — ElastiCache가 `transit_encryption_enabled=true`면 반드시 켜야 함(AUTH 토큰 여부와 별개) |
| `JOB_TRIGGER_QUEUE_URL` | 선택 | 비움 | `POST /v1/jobs`가 발행할 SQS 큐 URL — `infra/outputs.tf`의 `job_trigger_queue_url`. 비우면 `main.go`가 이 라우트 자체를 등록하지 않음(로컬 개발에 강제 안 함) |

CLI 플래그 없음. **주의**: `orders` 토픽은 자동 생성에 맡기면 안 되고 **정확히 20개 파티션**으로 미리 만들어둬야 함(마켓 1개=파티션 1개 전제, FR-11).

---

## 3. `matching/` (매칭 엔진, role B)

| 이름 | 필수 여부 | 로컬 기본값 | AWS에서 채울 값 |
|---|---|---|---|
| `KAFKA_BROKER` | **필수** | — | orderapi와 동일 브로커 |
| `REDIS_ADDR` | **필수** | — | orderapi와 동일 Redis |
| `ORDERS_TOPIC` | 선택 | `orders` | 그대로 둠 |
| `EXECUTIONS_TOPIC` | 선택 | `executions` | 그대로 둠 |
| `ASSIGNMENTS_TOPIC` | 선택 | `assignments` | 그대로 둠 |
| `KAFKA_USE_IAM_AUTH` | 선택 | `false` | orderapi와 동일 값 — 6번 문서 참고 |
| `REDIS_PASSWORD` | 선택 | 비움 | orderapi와 동일 값 |
| `REDIS_TLS_ENABLED` | 선택 | `false` | orderapi와 동일 값 |

CLI 플래그 없음. 인스턴스를 몇 개 띄우든(FR-11) 전부 같은 값을 보게 하면 됨 — 컨슈머 그룹 ID(`matching-engine`)는 코드 상수라 환경변수가 아님.

---

## 4. `trader/` (AI 트레이더)

| 이름 | 필수 여부 | 로컬 기본값 | AWS에서 채울 값 |
|---|---|---|---|
| `BACKEND_URL` | **필수** | — | 시세 수집기(`backend`) 엔드포인트 |
| `ORDERAPI_URL` | **필수** | — | `orderapi` 엔드포인트 |
| `BEDROCK_REGION` | **필수** | — | Bedrock을 실제로 호출할 리전 |
| `BEDROCK_MODEL_ID` | **필수** | — | Bedrock 콘솔에서 실제로 활성화한 모델 ID(또는 추론 프로파일 ID). 코드에 하드코딩 안 함 — 모델을 바꿔도 이 값만 바꾸면 됨 |

CLI 플래그(연결 정보가 아니라 실행 파라미터라 플래그로 유지):
| 플래그 | 필수 여부 | 기본값 | 의미 |
|---|---|---|---|
| `-date` | **필수** | — | 재생할 날짜(YYYY-MM-DD) |
| `-speed` | 선택 | `60` | 재생 배속 |
| `-order-bucket` | 선택 | `""`(비우면 로컬 `./orders`) | 주문 기록(FR-17)을 저장할 S3 버킷 — `team1-truss-order-records`(생성 완료, `-order-bucket=team1-truss-order-records`로 채우면 됨) |

AWS 자격증명은 SDK 기본 체인(인스턴스 프로파일/IRSA)을 그대로 씀 — 별도 액세스 키 환경변수 없음. `-order-bucket`을 채우거나 Bedrock을 실제로 호출하려면 이 실행 주체(EC2/Fargate/EKS Pod 등)에 해당 IAM 권한이 붙어 있어야 함(5번 문서 참고).

---

## 5. `replayengine/` (리플레이 엔진)

| 이름 | 필수 여부 | 로컬 기본값 | AWS에서 채울 값 |
|---|---|---|---|
| `ORDERAPI_URL` | **필수** | — | `orderapi` 엔드포인트 |

Kafka/Redis 환경변수 없음 — 리플레이 엔진은 그 둘을 직접 안 건드리고 접수 API만 호출.

CLI 플래그:
| 플래그 | 필수 여부 | 기본값 | 의미 |
|---|---|---|---|
| `-date` | **필수** | — | 재생할 기록의 날짜(trader가 그 날짜로 기록한 세션) |
| `-speed` | 선택 | `60` | 재생 배속 |
| `-order-bucket` | 선택 | `""`(비우면 로컬 `./orders`) | 주문 기록을 **읽어올** S3 버킷 — `team1-truss-order-records`(trader와 같은 버킷, 생성 완료) |
| `-shard-index` | 선택 | `0` | 분산 실행 시 이 인스턴스가 담당할 샤드 번호 |
| `-shard-count` | 선택 | `1` | 분산 실행 시 전체 인스턴스 수 |
| `-run-id` | 선택 | `""` | 같은 리플레이 실행에 속한 여러 샤드가 공유할 식별자 — **여러 인스턴스로 분산 실행할 때는 전부 같은 값을 줘야 함**(안 그러면 세션 가드가 서로 충돌한 걸로 보고 409를 냄) |
| `-from-ts`/`-to-ts` | 선택 | `0`(제한 없음) | 재생 구간 지정(Unix ms, FR-27) |

---

## 6. `recorder/` (기록기)

| 이름 | 필수 여부 | 로컬 기본값 | AWS에서 채울 값 |
|---|---|---|---|
| `PORT` | 선택 | `8082` | 조회 전용 HTTP API(`GET /v1/trace/{orderId}`, `GET /v1/matching/engines`)가 리슨할 포트 — `docs/frontend-backend-integration.md` 참고 |
| `KAFKA_BROKER` | **필수** | — | orderapi/matching과 동일 브로커 |
| `DATABASE_URL` | **필수** | — | `go-sql-driver/mysql` DSN 형식(**URL 아님**): `user:pass@tcp(host:port)/dbname?parseTime=true&loc=UTC` — 자체 호스팅 MySQL EC2(`team1-mysql`, `10.10.10.178:3306`) 엔드포인트로 채움. `parseTime=true`/`loc=UTC` 빠지면 타임스탬프 바인딩이 깨짐. 값 자체는 `recorder-db-secret` K8s Secret으로 주입 |
| `REDIS_ADDR` | **필수** | — | orderapi/matching과 동일 Redis |
| `ORDERS_TOPIC` | 선택 | `orders` | 그대로 둠 |
| `EXECUTIONS_TOPIC` | 선택 | `executions` | 그대로 둠 |
| `ASSIGNMENTS_TOPIC` | 선택 | `assignments` | 그대로 둠 |
| `ARCHIVE_BUCKET` | 선택 | `""`(비우면 로컬 `./records`) | `team1-truss-trade-results`(생성+Terraform 반영 완료) |
| `KAFKA_USE_IAM_AUTH` | 선택 | `false` | orderapi/matching과 동일 값 — 아래 참고 |
| `REDIS_PASSWORD` | 선택 | 비움 | orderapi/matching과 동일 값 |
| `REDIS_TLS_ENABLED` | 선택 | `false` | orderapi/matching과 동일 값 |

CLI 플래그 없음. **DB 스키마는 EC2 MySQL에서는 최초 기동 시 자동 적용됨** — `recorder/schema.sql`이 공식 `mysql` Docker 이미지의 `/docker-entrypoint-initdb.d/` 최초 기동 메커니즘으로 자동 실행된다(`infra/mysql-ec2/docker-compose.yml.tpl` 참고). 로컬 `infra/dev-mysql`은 이 메커니즘이 아직 없어 여전히 수동 적용이 필요함(Commands 절 참고).

**`schema.sql` 수동 적용이 필요했던 RDS 시절 절차는 [`recorder-schema-bootstrap.md`](recorder-schema-bootstrap.md)에 역사적 기록으로만 남아 있음** — 지금 프로덕션(EC2)에는 적용되지 않는다.

`DATABASE_URL`은 완성된 DSN 문자열 그대로 `recorder-db-secret`이라는 K8s Secret에 담겨 있고, `recorder/config.go`는 이걸 `os.Getenv`로 읽는다. 이 Secret은 Terraform이 관리한다(mysql-ec2.tf).

---

## 컨테이너 이미지

6개 모듈 전부 자체 `Dockerfile`(멀티스테이지: `golang:1.26-alpine` 빌드 → `alpine:3.20` 런타임)이 생겼고, `team1-truss` ECR 리포지토리(`infra/ecr.tf`)에 컴포넌트당 태그 하나로 푸시되어 있다.

| 모듈 | 이미지 | 컨테이너 실행 시 필요한 것 |
|---|---|---|
| `backend` | `...team1-truss:backend-latest` | 위 1번 표 env — HTTP 서버, `PORT` 포트로 리슨 |
| `trader` | `...team1-truss:trader-latest` | 위 4번 표 env + CLI 인자(`-date` 등) — **1회성 실행**(Job), 완료 후 종료 |
| `orderapi` | `...team1-truss:orderapi-latest` | 위 2번 표 env — HTTP 서버, `PORT` 포트로 리슨 |
| `matching` | `...team1-truss:matching-latest` | 위 3번 표 env — 포트 없음, Kafka/Redis만 |
| `replayengine` | `...team1-truss:replayengine-latest` | 위 5번 표 env + CLI 인자 — **1회성 실행**(Job), 완료 후 종료 |
| `recorder` | `...team1-truss:recorder-latest` | 위 6번 표 env — 포트 없음, Kafka/Redis/MySQL(EC2)만 |

`ENTRYPOINT`가 바이너리 자체이므로(예: `ENTRYPOINT ["/app/trader"]`) CLI 플래그는 K8s manifest의 `args`로, 연결 정보는 `env`로 각각 넘기면 된다 — 이 문서의 표 구분(플래그 vs 환경변수)이 그대로 `args`/`env` 구분과 일치한다. `trader`/`replayengine`은 `Job`으로(완료 후 종료가 정상), 나머지 넷은 `Deployment`로 띄우는 게 맞다(상시 실행).

## Kafka/Redis 인증

`orderapi`/`matching`/`recorder` 세 모듈 모두 **AWS_MSK_IAM(Kafka) + AUTH 토큰/TLS(Redis)** 인증을 지원한다. 전부 선택(optional) 환경변수라, 비워두면/`false`면 로컬 dev-kafka/dev-redis처럼 인증 없이 그대로 붙는다 — 즉 이 값들을 안 채워도 로컬 개발 워크플로는 전혀 바뀌지 않는다.

**Kafka(`KAFKA_USE_IAM_AUTH`)**: `true`면 AWS_MSK_IAM + TLS로 MSK에 인증한다. `infra/msk.tf`가 프로비저닝하는 건 MSK **Serverless**인데, AWS 공식 문서에 "MSK Serverless requires IAM access control for all clusters. Apache Kafka access control lists (ACLs) are not supported"라고 명시돼 있다 — 즉 MSK Serverless에서는 SASL/SCRAM 자체가 옵션이 아니다. `segmentio/kafka-go`는 메인 모듈이 아니라 `github.com/segmentio/kafka-go/sasl/aws_msk_iam_v2`라는 별도 서브모듈로 AWS_MSK_IAM을 지원한다 — AWS SDK v2 기반이라 이 프로젝트가 S3/Bedrock에서 이미 쓰는 자격증명 체인을 그대로 재사용한다. 자격증명은 AWS SDK v2 기본 체인(EC2 인스턴스 프로파일/EKS IRSA)을 그대로 쓴다 — 별도 사용자명/비밀번호가 필요 없다.

MSK 쪽에서 준비해야 할 것: EKS IRSA가 매핑된 IAM 역할에 `kafka-cluster:Connect`/`kafka-cluster:*Topic*`/`kafka-cluster:*Group*` 등 MSK IAM 정책 액션을 클러스터 ARN으로 스코프해서 붙여줘야 한다. Secrets Manager에 넣을 별도 비밀값은 필요 없다 — IAM 역할 자체가 자격증명이다.

**Redis(`REDIS_PASSWORD`/`REDIS_TLS_ENABLED`)**: `REDIS_PASSWORD`는 ElastiCache를 AUTH 토큰 있는 구성으로 만들고 그 토큰 값을 넘기면 된다(현재 `infra/elasticache.tf`가 이 구성). `REDIS_TLS_ENABLED`는 `transit_encryption_enabled=true`일 때 반드시 켜야 한다. 둘 다 `go-redis/v9`가 이미 지원하는 필드(`Password`/`TLSConfig`)라 별도 구현이 필요 없다.

**인프라 쪽에서 "VPC 프라이빗 서브넷 안이라 네트워크 격리로 충분하다"고 판단하면, 이 환경변수들을 그냥 비워두는 것도 유효한 선택이다** — 코드가 두 경로(인증 있음/없음) 모두 지원하므로 어느 쪽으로 가든 코드 변경이 필요 없다. 다만 MSK Serverless는 이 선택권이 없다(IAM 필수) — 위 참고.
