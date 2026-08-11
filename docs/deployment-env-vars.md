# 배포 환경변수 / CLI 플래그 정리

## 변경 이력
| 날짜 | 변경 내용 |
|---|---|
| 2026-08-11 | `KAFKA_SASL_USERNAME`/`KAFKA_SASL_PASSWORD` 삭제, `KAFKA_USE_IAM_AUTH`로 교체(SCRAM→AWS_MSK_IAM 전환 — `infra/msk.tf`가 MSK **Serverless**를 쓰는데, MSK Serverless는 IAM 인증만 지원하고 SASL/SCRAM 자체를 지원하지 않는다는 걸 AWS 공식 문서로 확인함). `REDIS_TLS_ENABLED` 신설(`infra/elasticache.tf`가 `transit_encryption_enabled=true`+AUTH 토큰 없음 조합을 쓰는데, 기존 `REDIS_PASSWORD` 하나로는 이 조합을 표현할 수 없었음) |
| 2026-08-10 (2차) | Kafka SASL/SCRAM(`KAFKA_SASL_USERNAME`/`KAFKA_SASL_PASSWORD`) + Redis AUTH(`REDIS_PASSWORD`) 환경변수 추가 — orderapi/matching/recorder 3개 모듈 전부. 기존 "코드에 인증 지원 없음" 참고 섹션은 삭제(이제 지원함). **2026-08-11에 Kafka 쪽은 다시 교체됨(위 항목 참고)** |
| 2026-08-10 | 최초 작성 — AWS에서 6개 Go 모듈을 실제로 돌릴 때 필요한 환경변수/플래그 전체 정리 (AI 트레이더 Bedrock 연동 작업 직후) |

## 관련 문서
- [CLAUDE.md](../CLAUDE.md) — 각 모듈의 Commands/Architecture 섹션에 있는 원본 설명
- [인프라 배치 설계](infra-placement-design.md) — 이 값들이 실제로 어디(MSK/RDS/ElastiCache 등)를 가리켜야 하는지

이 문서는 **각 모듈의 `config.go`/`main.go`를 직접 읽고 정리한 것**이라 CLAUDE.md의 서술형 설명보다 이 값들 자체를 빠르게 확인하고 싶을 때 쓰는 표 위주 참조 문서다. 로컬 개발용 `.env` 값(예: `localhost:9092`)이 아니라 **AWS에 배포할 때 채워야 하는 실제 값의 형태**를 기준으로 적었다.

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
| `EXECUTIONS_TOPIC` | 선택 | `executions` | 그대로 둠(고정 관례값, 2026-08-10 체결 반영 기능 추가로 신설) |
| `KAFKA_USE_IAM_AUTH` | 선택 (2026-08-11 추가) | `false` | `true`면 AWS_MSK_IAM으로 MSK에 인증(자격증명은 SDK 기본 체인/EKS IRSA) — 6번 문서 "Kafka/Redis 인증" 참고. `false`면 인증 없이 붙음(로컬 그대로) |
| `REDIS_PASSWORD` | 선택 (2026-08-10 추가) | 비움 | ElastiCache AUTH 토큰. 비우면 인증 없이 붙음 |
| `REDIS_TLS_ENABLED` | 선택 (2026-08-11 추가) | `false` | `true`면 Redis 연결에 TLS 사용 — ElastiCache가 `transit_encryption_enabled=true`면 반드시 켜야 함(AUTH 토큰 여부와 별개) |

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
| `KAFKA_USE_IAM_AUTH` | 선택 (2026-08-11 추가) | `false` | orderapi와 동일 값 — 6번 문서 참고 |
| `REDIS_PASSWORD` | 선택 (2026-08-10 추가) | 비움 | orderapi와 동일 값 |
| `REDIS_TLS_ENABLED` | 선택 (2026-08-11 추가) | `false` | orderapi와 동일 값 |

CLI 플래그 없음. 인스턴스를 몇 개 띄우든(FR-11) 전부 같은 값을 보게 하면 됨 — 컨슈머 그룹 ID(`matching-engine`)는 코드 상수라 환경변수가 아님.

---

## 4. `trader/` (AI 트레이더)

| 이름 | 필수 여부 | 로컬 기본값 | AWS에서 채울 값 |
|---|---|---|---|
| `BACKEND_URL` | **필수** | — | 시세 수집기(`backend`) 엔드포인트 |
| `ORDERAPI_URL` | **필수** | — | `orderapi` 엔드포인트 |
| `BEDROCK_REGION` | **필수** (2026-08-10 추가) | — | Bedrock을 실제로 호출할 리전 — `ap-northeast-2`가 될지 크로스 리전 프로파일이 필요해 다른 리전이 될지 인프라 쪽에서 확인 필요([인프라 배치 설계 7장](infra-placement-design.md) 참고 |
| `BEDROCK_MODEL_ID` | **필수** (2026-08-10 추가) | — | Bedrock 콘솔에서 실제로 활성화한 모델 ID(또는 추론 프로파일 ID). 코드에 하드코딩 안 함 — 모델을 바꿔도 이 값만 바꾸면 됨 |

CLI 플래그(연결 정보가 아니라 실행 파라미터라 플래그로 유지):
| 플래그 | 필수 여부 | 기본값 | 의미 |
|---|---|---|---|
| `-date` | **필수** | — | 재생할 날짜(YYYY-MM-DD) |
| `-speed` | 선택 | `60` | 재생 배속 |
| `-order-bucket` | 선택 | `""`(비우면 로컬 `./orders`) | 주문 기록(FR-17)을 저장할 S3 버킷 — `team1-truss-order-records`(아직 미생성, 5번 문서 참고) |

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
| `-order-bucket` | 선택 | `""`(비우면 로컬 `./orders`) | 주문 기록을 **읽어올** S3 버킷 — trader와 같은 버킷 |
| `-shard-index` | 선택 | `0` | 분산 실행 시 이 인스턴스가 담당할 샤드 번호 |
| `-shard-count` | 선택 | `1` | 분산 실행 시 전체 인스턴스 수 |
| `-run-id` | 선택 | `""` | 같은 리플레이 실행에 속한 여러 샤드가 공유할 식별자 — **여러 인스턴스로 분산 실행할 때는 전부 같은 값을 줘야 함**(안 그러면 세션 가드가 서로 충돌한 걸로 보고 409를 냄) |
| `-from-ts`/`-to-ts` | 선택 | `0`(제한 없음) | 재생 구간 지정(Unix ms, FR-27) |

---

## 6. `recorder/` (기록기)

| 이름 | 필수 여부 | 로컬 기본값 | AWS에서 채울 값 |
|---|---|---|---|
| `KAFKA_BROKER` | **필수** | — | orderapi/matching과 동일 브로커 |
| `DATABASE_URL` | **필수** | — | `go-sql-driver/mysql` DSN 형식(**URL 아님**): `user:pass@tcp(host:port)/dbname?parseTime=true&loc=UTC` — RDS MySQL 엔드포인트로 채움. `parseTime=true`/`loc=UTC` 빠지면 타임스탬프 바인딩이 깨짐 |
| `REDIS_ADDR` | **필수** | — | orderapi/matching과 동일 Redis |
| `ORDERS_TOPIC` | 선택 | `orders` | 그대로 둠 |
| `EXECUTIONS_TOPIC` | 선택 | `executions` | 그대로 둠 |
| `ASSIGNMENTS_TOPIC` | 선택 | `assignments` | 그대로 둠 |
| `ARCHIVE_BUCKET` | 선택 | `""`(비우면 로컬 `./records`) | `team1-truss-trade-results`(이미 AWS엔 있음, Terraform state엔 아직 없음 — 5번 문서 참고) |
| `KAFKA_USE_IAM_AUTH` | 선택 (2026-08-11 추가) | `false` | orderapi/matching과 동일 값 — 아래 참고 |
| `REDIS_PASSWORD` | 선택 (2026-08-10 추가) | 비움 | orderapi/matching과 동일 값 |
| `REDIS_TLS_ENABLED` | 선택 (2026-08-11 추가) | `false` | orderapi/matching과 동일 값 |

CLI 플래그 없음. **DB 스키마는 자동 마이그레이션이 없음** — `recorder/schema.sql`을 RDS에 최초 1회 수동 적용해야 함(`mysql -h<RDS엔드포인트> -u... -p... <db> < schema.sql`).

---

## Kafka/Redis 인증 (2026-08-10 추가, Kafka 쪽 2026-08-11 교체)

`orderapi`/`matching`/`recorder` 세 모듈 모두 **AWS_MSK_IAM(Kafka) + AUTH 토큰/TLS(Redis)** 인증을 지원한다. 전부 선택(optional) 환경변수라, 비워두면/`false`면 로컬 dev-kafka/dev-redis처럼 인증 없이 그대로 붙는다 — 즉 이 값들을 안 채워도 로컬 개발 워크플로는 전혀 바뀌지 않는다.

**Kafka(`KAFKA_USE_IAM_AUTH`)**: `true`면 AWS_MSK_IAM + TLS로 MSK에 인증한다. **2026-08-10에는 SASL/SCRAM을 먼저 택했다가 2026-08-11에 IAM으로 교체했다** — 이유는 인프라 쪽 사정이 아니라 MSK 자체의 제약이다: `infra/msk.tf`가 실제로 프로비저닝하는 건 MSK **Serverless**인데, AWS 공식 문서에 "MSK Serverless requires IAM access control for all clusters. Apache Kafka access control lists (ACLs) are not supported"라고 명시돼 있다 — 즉 MSK Serverless에서는 SASL/SCRAM 자체가 옵션이 아니다. (SCRAM을 골랐던 원래 근거 — "`segmentio/kafka-go`가 AWS_MSK_IAM을 지원하지 않는다" — 도 다시 확인해보니 틀렸다: 메인 모듈만 봐서 놓친 것이고, 실제로는 `github.com/segmentio/kafka-go/sasl/aws_msk_iam_v2`라는 별도 서브모듈로 2023년부터 존재한다. AWS SDK v2 기반이라 이 프로젝트가 S3/Bedrock에서 이미 쓰는 자격증명 체인을 그대로 재사용할 수 있어서, 직접 프로토콜을 구현하는 것과는 리스크가 다르다.) 자격증명은 AWS SDK v2 기본 체인(EC2 인스턴스 프로파일/EKS IRSA)을 그대로 쓴다 — 별도 사용자명/비밀번호가 필요 없다.

MSK 쪽에서 준비해야 할 것: EKS IRSA가 매핑된 IAM 역할에 `kafka-cluster:Connect`/`kafka-cluster:*Topic*`/`kafka-cluster:*Group*` 등 MSK IAM 정책 액션을 클러스터 ARN으로 스코프해서 붙여줘야 한다. Secrets Manager에 넣을 별도 비밀값은 필요 없다 — IAM 역할 자체가 자격증명이다.

**Redis(`REDIS_PASSWORD`/`REDIS_TLS_ENABLED`)**: `REDIS_PASSWORD`는 ElastiCache를 AUTH 토큰 있는 구성으로 만들고 그 토큰 값을 넘기면 된다. `REDIS_TLS_ENABLED`는 2026-08-11에 별도로 추가했다 — `infra/elasticache.tf`가 `transit_encryption_enabled=true`인데 `auth_token`은 안 두는 구성이라("TLS는 필수, 비밀번호는 없음"), 기존 `REDIS_PASSWORD` 하나만으로는 이 조합을 표현할 수 없었다. 둘 다 `go-redis/v9`가 이미 지원하는 필드(`Password`/`TLSConfig`)라 별도 구현이 필요 없었다.

**인프라 쪽에서 "VPC 프라이빗 서브넷 안이라 네트워크 격리로 충분하다"고 판단하면, 이 환경변수들을 그냥 비워두는 것도 유효한 선택이다** — 코드가 두 경로(인증 있음/없음) 모두 지원하므로 어느 쪽으로 가든 코드 변경이 필요 없다. 다만 MSK Serverless는 이 선택권이 없다(IAM 필수) — 위 참고.
