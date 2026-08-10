# 배포 환경변수 / CLI 플래그 정리

## 변경 이력
| 날짜 | 변경 내용 |
|---|---|
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

CLI 플래그 없음. **DB 스키마는 자동 마이그레이션이 없음** — `recorder/schema.sql`을 RDS에 최초 1회 수동 적용해야 함(`mysql -h<RDS엔드포인트> -u... -p... <db> < schema.sql`).

---

## 참고: Kafka/Redis 인증에 대한 현재 코드의 전제

지금 6개 모듈의 Kafka 클라이언트(`segmentio/kafka-go`)는 전부 **SASL/TLS 설정이 없다** — 브로커에 평문 TCP로 그냥 붙는다. Redis 클라이언트(`go-redis/v9`)도 마찬가지로 **비밀번호/AUTH 설정이 없다**.

즉:
- `infra-placement-design.md`가 제안한 **MSK Serverless + SASL/IAM 인증**을 그대로 쓰면, 지금 코드로는 연결이 안 된다 — `github.com/aws/aws-msk-iam-sasl-signer-go` 같은 걸 붙이는 **코드 작업이 먼저 필요**하다.
- ElastiCache를 **AUTH 토큰 있는 구성**으로 만들면 마찬가지로 코드 쪽에 `REDIS_PASSWORD` 같은 값을 추가해야 한다.

인프라 쪽에서 "VPC 프라이빗 서브넷 안이라 네트워크 격리로 충분하다"고 판단해 인증 없는 Kafka/Redis(보안 그룹으로만 접근 제어)로 간다면 지금 코드가 그대로 동작한다. IAM 인증/AUTH 토큰을 쓰기로 하면, 그건 이 문서의 범위를 넘는 별도 코드 작업이 필요하다는 것만 미리 알아두면 됨.
