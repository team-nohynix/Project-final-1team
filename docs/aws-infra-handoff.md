# AWS 리소스 · 권한 · 계정 정리 (인프라 팀원 전달용)

> 이 문서는 요구사항/판단 근거를 정리한 핸드오프 문서다. 지금 실제 인프라 상태는
> [`../CLAUDE.md`](../CLAUDE.md)의 "Infra (Terraform)" 절이 더 정확하다.

## 0. 계정 / 리전

- 계정 `727646470302` — **여러 팀이 공유**. 모든 리소스는 `team1-*`로 네임스페이스.
- 리전 `ap-northeast-2`(서울) — **Bedrock만 예외일 수 있음**, 3번 참고.
- Terraform 상태: `team1-terraform-state-s3` 버킷(S3). 새 스택을 만들면 `key = "<스택명>/terraform.tfstate"`로 같은 버킷에 추가.

## 1. 컴퓨트 / 네트워크

단일 EKS 클러스터 — 관리형 노드그룹(백엔드: 접수API·매칭엔진·기록기)과 Fargate Profile
3종(시세수집기/AI트레이더/리플레이, 네임스페이스로 격리)으로 워크로드만 분리한다.

Go 바이너리라 정적 컴파일된 단일 실행 파일 배포가 가능함 — 컨테이너화한다면
`scratch`/`alpine` 같은 매우 가벼운 베이스로 충분.

## 2. S3 버킷

| 버킷 | 쓰는 쪽 |
|---|---|
| `team1-truss-market-data` | `backend`(쓰기/읽기) |
| `team1-truss-trade-results` | `recorder`(쓰기) |
| `team1-truss-order-records` | `trader`(쓰기), `replayengine`(읽기) |

버킷 설정: SSE-S3(AES256) 암호화, 퍼블릭 액세스 전면 차단, **라이프사이클 규칙 없음**(같은
데이터로 인프라 변경 전후 성능을 비교해야 해서 만료/전환 금지).

## 3. AWS Bedrock

- 모델 액세스 활성화 필요 — 앤트로픽 Claude 계열(팀 결정: 가장 저렴한 모델).
- 리전: 코드는 `BEDROCK_REGION` 환경변수로 리전을 따로 받으므로, 온디맨드 직접 호출이
  막힌 모델은 크로스 리전 추론 프로파일(APAC 등)로 대응 가능 — IAM 정책의 리소스 ARN을
  그 리전/추론 프로파일에 맞게 지정해야 한다.
- IAM 권한: `trader`가 쓰는 역할에 `bedrock:InvokeModel`을 특정 모델/추론 프로파일 ARN으로
  제한해서 부여(계정 공유 환경).
- 이 권한은 **`trader`에만** 필요하다 — 다른 모듈은 Bedrock을 호출하지 않는다.

## 4. IAM — 역할 정리

| 역할 | 쓰는 모듈 | 필요 권한 |
|---|---|---|
| `sa-collector` | `backend` | S3 `market-data` Get/Put/List |
| `sa-ai-trader` | `trader` | S3 `order-records` Get/Put, Bedrock `InvokeModel`(3번 참고) |
| `sa-replay-engine` | `replayengine` | S3 `order-records` Get |
| `sa-recorder` | `recorder` | S3 `trade-results` Put, MSK IAM, Secrets Manager 읽기 |
| `sa-ingest-api`/`sa-matching-engine` | `orderapi`/`matching` | MSK IAM(5번 참고), Redis AUTH 토큰(Secrets Manager, 6번 참고) |

각 역할은 EKS IRSA로 서비스 어카운트에 매핑한다(`infra/irsa.tf`).

## 5. Kafka — AWS_MSK_IAM

`infra/msk.tf`가 프로비저닝하는 MSK **Serverless**는 IAM 인증만 지원한다(ACL/SASL 불가,
AWS 공식 문서 명시) — 선택지가 아니라 필수.

- 필요한 것: 브로커 엔드포인트 1개(`KAFKA_BROKER`), `orders`/`executions`/`assignments` 3개
  토픽. **`orders`는 자동 생성에 맡기면 안 되고 정확히 20개 파티션으로 미리 만들어야 한다**
  (마켓 1개 = 파티션 1개 전제).
- IAM 권한: `orderapi`/`matching`/`recorder`가 쓰는 역할에 `kafka-cluster:Connect`,
  `kafka-cluster:DescribeTopic`/`WriteData`/`ReadData`, `kafka-cluster:AlterGroup`/
  `DescribeGroup` 등을 클러스터 ARN으로 스코프해서 부여. 별도 사용자명/비밀번호나
  Secrets Manager 저장이 필요 없다 — IAM 역할 자체가 자격증명이라 앱 쪽 환경변수는
  `KAFKA_USE_IAM_AUTH=true` 하나뿐.
- 앱 코드는 `github.com/segmentio/kafka-go/sasl/aws_msk_iam_v2` 서브모듈로 인증한다 — AWS
  SDK v2 기반이라 S3/Bedrock에 쓰는 것과 같은 자격증명 체인(EC2 인스턴스 프로파일/IRSA)을
  재사용한다.

## 6. Redis — AUTH 토큰 + TLS

- 필요한 것: 엔드포인트 1개(`REDIS_ADDR`). `orderapi`(세션 가드, 백프레셔 체크, 호가창
  캐시), `matching`(스냅샷, 백프레셔, 부하 추적), `recorder`(백프레셔 감시)가 전부 같은
  인스턴스를 씀.
- `infra/elasticache.tf`는 `transit_encryption_enabled=true` + `auth_token` 둘 다 켠다 —
  `REDIS_TLS_ENABLED=true`, `REDIS_PASSWORD`는 Secrets Manager(`team1/backend/redis-auth-token`)
  경유로 채운다. 둘 다 `go-redis/v9`가 이미 지원하는 필드(`Password`/`TLSConfig`)다.

## 7. DB — MySQL, EC2 자체 호스팅(RDS 아님)

- 엔진: **MySQL 8.4**, 공식 `mysql` Docker 이미지로 EC2 인스턴스 위에서 직접 운영
  (`team1-mysql`, `m6i.2xlarge`, `eks_backend` 서브넷).
- `recorder`가 `DATABASE_URL`로 받는 값은 URL이 아니라 `go-sql-driver/mysql`의 DSN 형식:
  `user:pass@tcp(host:port)/dbname?parseTime=true&loc=UTC`.
- 스키마는 공식 `mysql` 이미지의 `/docker-entrypoint-initdb.d/` 최초 기동 메커니즘으로
  자동 적용된다(`recorder-schema-bootstrap.md` 참고).
- 자격증명은 `recorder-db-secret`이라는 K8s Secret(`DATABASE_URL` 키, 완성된 DSN)으로
  전달 — Terraform이 관리한다(`mysql-ec2.tf`).
- 네트워크: 보안그룹 `team1_sg_mysql_ec2`가 `eks_backend` 보안그룹에서만 3306을 허용,
  퍼블릭 액세스 없음.
