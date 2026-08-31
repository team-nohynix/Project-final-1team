# Truss (Project-final-1team)

쿠버네티스 환경에서 대량 주문 상황에 거래 시스템이 얼마나 견디는지 실측하는 인프라 부하테스트 프로젝트다. 업비트 실거래 데이터를 기반으로 AI 트레이더가 주문을 생성하고, 접수 API·매칭 엔진이 이를 처리하며, 리플레이 엔진으로 동일 주문 패턴을 반복 재생해 성능을 측정한다. 요구사항/설계의 자세한 내용은 [docs/requirements.md](docs/requirements.md)를 참고한다.

## 프로젝트 구조

독립된 Go 모듈 6개 + 프론트엔드 1개로 구성된다. 각 Go 모듈은 자체 `go.mod`를 가진 별도 모듈이라, 작업할 모듈 디렉터리로 `cd`한 뒤 Go 명령을 실행해야 한다(루트에 모듈을 묶는 워크스페이스 파일은 없음).

| 디렉터리 | 역할 |
|---|---|
| `backend/` | 시세 수집기 — 업비트 시세 수집·저장, 트레이더에게 제공 |
| `trader/` | 페이퍼 트레이딩 엔진 — AI 트레이더 봇이 시세를 보고 주문 생성 |
| `orderapi/` | 주문 접수 API (역할 A) |
| `matching/` | 매칭 엔진 (역할 B) |
| `replayengine/` | 리플레이 엔진 — 기록된 주문을 판단 로직 없이 그대로 재생해 부하 발생 |
| `recorder/` | 기록기 — 주문/체결 결과를 MySQL·S3에 저장 |
| `frontend/` | 모니터링/운영 화면 (Vue 3 + Vite) — 자체 `frontend/README.md` 참고 |
| `infra/` | Terraform + Kubernetes 매니페스트 |
| `docs/` | 요구사항·API·아키텍처 등 설계 문서 |

프로젝트 전반의 아키텍처·설계 결정·컨벤션은 [CLAUDE.md](CLAUDE.md)에 훨씬 자세히 정리되어 있다 — 특정 기능이 왜 이렇게 만들어졌는지 궁금하면 먼저 그 문서를 확인한다.

## 사전 준비

1. **Go** — [go.dev/dl](https://go.dev/dl/)에서 설치. `go version`으로 확인.
2. **Node.js/npm** — 프론트엔드 실행용.
3. **Docker Desktop** — 로컬 Kafka/Redis/MySQL을 컨테이너로 띄우는 데 필요.
4. 편집기는 VS Code 권장 — Go 확장(`Go` by Go Team at Google), Vue 확장(`Vue - Official`, Volar) 설치.

## 로컬 인프라 (Kafka / Redis / MySQL)

`orderapi`/`matching`/`recorder`를 실행하려면 세 인프라가 모두 로컬에 떠 있어야 한다.

```
cd infra/dev-kafka && docker compose up -d
cd infra/dev-redis && docker compose up -d
cd infra/dev-mysql && docker compose up -d
```

- **Kafka**: `orders` 토픽은 마켓 20개와 1:1 대응하도록 **파티션 20개로 직접 생성**해야 한다(자동 생성 시 기본 1개라 FR-11 마켓 재분배가 정상 동작하지 않는다).
  ```
  kafka-topics.sh --create --topic orders --partitions 20 --replication-factor 1
  ```
- **MySQL**: 스키마가 자동 적용되지 않는다. 컨테이너가 뜬 뒤 한 번 수동으로 적용한다.
  ```
  cat recorder/schema.sql | docker exec -i dev-mysql-mysql-1 mysql -u"$MYSQL_USER" -p"$MYSQL_PASSWORD" "$MYSQL_DATABASE"
  ```
  `infra/dev-mysql/.env`(gitignored)에 `MYSQL_ROOT_PASSWORD`/`MYSQL_DATABASE`/`MYSQL_USER`/`MYSQL_PASSWORD`를 직접 채워 넣어야 컨테이너가 뜬다.

## 모듈별 실행 방법

각 모듈 디렉터리에 `.env` 파일(gitignored, 직접 생성 필요)이 필요하다. 필수 항목이 비어 있으면 프로그램이 즉시 종료된다(fail-fast 원칙 — [CLAUDE.md](CLAUDE.md) Conventions 참고).

### backend (시세 수집기)
```
cd backend
go run .            # PORT=8080
```
`.env`: `APP_ENV`(필수, dev/prod), `PORT`(선택, 기본 8080), `S3_BUCKET`(APP_ENV=prod일 때만 필수 — dev는 로컬 디스크에 저장).

### orderapi (주문 접수 API)
```
cd orderapi
go run .            # PORT=8081, KAFKA_BROKER 필요
```
`.env`: `KAFKA_BROKER`(필수), `REDIS_ADDR`(필수), 그 외 `PORT`/`ORDERS_TOPIC`/`EXECUTIONS_TOPIC` 등은 선택(기본값 있음).

### matching (매칭 엔진)
```
cd matching
go run .            # KAFKA_BROKER, REDIS_ADDR 필요
```

### trader (페이퍼 트레이딩 엔진)
```
cd trader
go run . -date=2026-07-27 -speed=60
```
`.env`: `BACKEND_URL`/`ORDERAPI_URL`/`BEDROCK_REGION`/`BEDROCK_MODEL_ID` 모두 필수. `-date`/`-speed`는 실행 시마다 바뀌는 값이라 커맨드라인 인자로 넘긴다(env var가 아님).

### replayengine (리플레이 엔진)
```
cd replayengine
go run . -date=2026-08-04 -speed=500
```
`.env`: `ORDERAPI_URL` 필수.

### recorder (기록기)
```
cd recorder
go run .            # PORT=8082, KAFKA_BROKER/DATABASE_URL/REDIS_ADDR 필요
```
`DATABASE_URL`은 URL이 아니라 `go-sql-driver/mysql` DSN 형식이다: `user:pass@tcp(localhost:3306)/dbname?parseTime=true&loc=UTC`.

각 모듈이 실제로 읽는 전체 환경변수 목록과 로컬/배포 값은 [docs/deployment-env-vars.md](docs/deployment-env-vars.md)에 정리되어 있다.

## 프론트엔드 실행

```
cd frontend
npm install
npm run dev
```
필요하면 `frontend/.env.example`을 참고해 `.env`를 만든다. 자세한 내용은 [frontend/README.md](frontend/README.md) 참고.

## 테스트

```
go build ./... && go vet ./... && go test ./...
```
6개 Go 모듈 각각에서 실행한다. 순수 로직(디코딩, 상태 전이, 배치 트리거 등)은 단위 테스트로 커버되어 있고, 실제 Kafka/Redis/MySQL/S3를 건드리는 코드는 로컬 인프라를 띄운 뒤 손으로 검증하는 것이 이 프로젝트의 관례다 — 근거는 [CLAUDE.md](CLAUDE.md)의 Tests 섹션 참고.

## 더 읽을 문서

- [docs/requirements.md](docs/requirements.md) — 요구사항 정의서 + 기능 목록
- [docs/api-specification.md](docs/api-specification.md) — 접수 API 규격
- [docs/erd.md](docs/erd.md) — DB 스키마 설계
- [docs/security-design.md](docs/security-design.md) — 보안 및 권한 설계
- [docs/aws-infra-handoff.md](docs/aws-infra-handoff.md) / [docs/deployment-env-vars.md](docs/deployment-env-vars.md) — AWS 배포 시 필요한 리소스·권한·환경변수
- [docs/frontend-backend-integration.md](docs/frontend-backend-integration.md) — 프론트엔드-백엔드 연동 현황
- [CLAUDE.md](CLAUDE.md) — 아키텍처·설계 결정·컨벤션 전체 (가장 상세함)
