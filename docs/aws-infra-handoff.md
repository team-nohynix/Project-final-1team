# AWS 리소스 · 권한 · 계정 정리 (인프라 팀원 전달용)

> **이 문서는 2026-08-10~11 시점의 스냅샷이다 — "1. 현재 상태" 표를 포함해 아래 내용 상당수는 실제 인프라가 만들어지기 전에 "무엇을 프로비저닝해야 하는지" 정리한 핸드오프 문서로 작성됐다.** 그 이후 EKS(단일 클러스터)/MSK/RDS(MySQL, Multi-AZ)/ElastiCache/CloudFront가 전부 실제로 적용됐다 — 지금 실제 인프라 상태는 [`../CLAUDE.md`](../CLAUDE.md)의 "Infra (Terraform)" 절이 더 정확하다. 이 문서는 그때의 요구사항/판단 기록으로만 참고할 것.

## 변경 이력
| 날짜 | 변경 내용 |
|---|---|
| 2026-08-11 | **Kafka 인증을 SASL/SCRAM에서 AWS_MSK_IAM으로 재교체** — `infra/msk.tf`를 실제로 읽어보니 MSK **Serverless**를 프로비저닝하는데, AWS 공식 문서에 "MSK Serverless requires IAM access control for all clusters, ACLs not supported"라고 명시돼 있어 SASL/SCRAM 자체가 옵션이 아니었다. 인프라를 Provisioned로 바꾸기보다 앱 코드를 IAM으로 바꾸는 쪽으로 결정(5번 섹션 다시 씀). Redis 쪽은 `infra/elasticache.tf`가 `transit_encryption_enabled=true`+AUTH 토큰 없음 조합이라 `REDIS_TLS_ENABLED`를 신설(6번 섹션 갱신). |
| 2026-08-10 (3차) | Kafka/Redis 인증 지원 완료 — orderapi/matching/recorder 3개 모듈 모두 SASL/SCRAM-SHA-512(Kafka) + AUTH 토큰(Redis)을 지원하게 코드를 고쳤다. 5·6번 섹션을 "지금 코드는 인증 지원 안 함"에서 "이렇게 인증을 켜면 된다"로 다시 씀. `infra-placement-design.md`의 RDS 엔진(PostgreSQL→MySQL)뿐 아니라 Kafka 보안 그룹 매트릭스의 "9098 (IAM)" 표기도 이번에 SCRAM으로 정정. **2026-08-11에 다시 IAM으로 뒤집힘(위 항목 참고) — 9098/IAM 표기가 오히려 맞았던 것으로 정정.** |
| 2026-08-10 (2차) | 최초 작성 후 발견: RoundTripper 인터페이스에 nil 포인터를 담아 넘기면(콘크리트 타입 nil→인터페이스 nil 아님) 인증 없는 로컬 환경에서 오히려 패닉이 나는 버그를 실제로 겪고 고침(코드 쪽 이슈, 이 문서엔 직접 영향 없음) |
| 2026-08-10 | 최초 작성 — 현재 실제 구현(Go 6개 모듈, MySQL로 전환된 recorder, 2026-08-10 추가된 Bedrock 연동)을 기준으로 필요한 AWS 리소스/권한을 정리. 기존 설계 문서와의 불일치 2건 발견 — 같은 날 `ai-trader-design.md`의 Python 표기는 Go로 정정했음(아래 1번은 해결됨). `infra-placement-design.md`의 RDS 엔진 표기(2번)는 아직 안 고침 |

## 먼저 확인해줄 것 — 기존 설계 문서와 실제 구현이 다른 부분 (당시 기준, 지금은 모두 해결됨)

1. ~~`ai-trader-design.md`는 Python 스택을 전제로 쓰여 있었는데~~ — **2026-08-10에 Go로 정정 완료.**
2. ~~설계 문서가 RDS를 PostgreSQL로 설계해뒀던 것~~ — **RDS는 MySQL(Multi-AZ DB 클러스터)로 적용 완료.**
3. ~~설계 문서가 EKS를 역할별 3개 클러스터로 분리해뒀던 것~~ — 멘토링 반려 후 **단일 EKS 클러스터**(관리형 노드그룹 + Fargate Profile 3종으로 워크로드만 격리)로 재설계·적용 완료.

이 문서 작성 시점엔 **VPC(퍼블릭 서브넷 1개)와 S3 버킷 2개, IAM 역할 1개뿐**이었지만(아래 "현재 상태" 표는 그 시점 기록), 지금은 EKS/MSK/ElastiCache/CloudFront가 전부 실제로 적용돼 있고, DB는 RDS에서 자체 호스팅 MySQL(EC2)로 전환되어 적용돼 있다 — 위 안내 배너와 [`../CLAUDE.md`](../CLAUDE.md) 참고.

---

## 0. 계정 / 리전

- 계정 `727646470302` — **여러 팀이 공유**. 모든 리소스는 `team1-*`로 네임스페이스(기존 관례).
- 리전 `ap-northeast-2`(서울) — **Bedrock만 예외일 수 있음**, 3번 참고.
- Terraform 상태: `team1-terraform-state-s3` 버킷(S3, 이미 구성됨). 새 스택을 만들면 `key = "<스택명>/terraform.tfstate"`로 같은 버킷에 추가.
- Terraform 실행 주체: 공유 학생 IAM 사용자(`a-student-05`) — 애플리케이션 런타임 자격증명과는 별개.

## 1. 현재 상태 (이미 있는 것)

| 리소스 | 상태 |
|---|---|
| VPC(`10.100.0.0/16`) + 퍼블릭 서브넷 1개(`ap-northeast-2a`) | 있음(`infra/network.tf`) — 프라이빗 서브넷/NAT/2번째 AZ 없음 |
| S3 `team1-truss-market-data` | 있음, Terraform 관리됨, IAM 스코프 완료 |
| S3 `team1-truss-trade-results` | **AWS에는 있지만 Terraform state엔 아직 없음**(2026-08-10 `.tf`만 추가, import는 보류) |
| IAM `team1_ec2_role`/`team1_ec2_s3_policy`/`team1_ec2_profile` | 있음, `team1-truss-market-data` 버킷에만 스코프(backend 전용) |
| EKS / MSK / RDS / ElastiCache / CloudFront | **없음** |

## 2. S3 버킷 — 필요한 것

| 버킷 | 상태 | 쓰는 쪽 | 필요한 권한 |
|---|---|---|---|
| `team1-truss-market-data` | 있음 | `backend` | 이미 완료 |
| `team1-truss-trade-results` | AWS엔 있음, Terraform import 필요 | `recorder` (쓰기) | **아직 IAM 정책 없음** — 신규 필요 |
| `team1-truss-order-records` | **아직 생성 안 됨** | `trader`(쓰기), `replayengine`(읽기) | 버킷 신규 생성 + IAM 정책 신규 필요 |

버킷 설정은 기존 `team1-truss-market-data`와 동일하게: SSE-S3(AES256) 암호화, 퍼블릭 액세스 전면 차단, **라이프사이클 규칙 없음**(같은 데이터로 인프라 변경 전후 성능을 비교해야 해서 만료/전환 금지).

## 3. AWS Bedrock — 2026-08-10부터 새로 필요

- **모델 액세스 활성화 필요**: Bedrock 콘솔에서 앤트로픽 Claude 계열 모델(팀 결정: 가장 저렴한 모델, 즉 Haiku 계열) 액세스를 켜야 한다.
- **리전 확인 필요** — 아직 미확인: 그 모델이 `ap-northeast-2`에서 온디맨드로 바로 되는지, 안 되면 크로스 리전 추론 프로파일(예: APAC 프로파일)이 필요한지 Bedrock 콘솔에서 먼저 확인해야 한다. 다른 리소스는 전부 `ap-northeast-2`인데 Bedrock만 리전이 다를 수 있다는 뜻 — 코드는 `BEDROCK_REGION` 환경변수로 리전을 따로 받으므로 코드 변경 없이 대응 가능하지만, IAM 정책의 리소스 ARN을 그 리전/추론 프로파일에 맞게 지정해야 한다.
- **IAM 권한**: `trader`가 쓰는 역할에 `bedrock:InvokeModel`(Converse API도 이 액션 하나로 커버됨)을 부여. **특정 모델 ARN으로 제한**하는 걸 권장(계정 공유 환경이라 다른 팀/다른 모델 호출까지 열어줄 필요 없음).
- 이 권한은 **`trader`에만** 필요하다 — `orderapi`/`matching`/`replayengine`/`recorder`는 Bedrock을 호출하지 않는다.

## 4. IAM — 필요한 역할 정리

| 역할(가칭) | 쓰는 모듈 | 필요 권한 |
|---|---|---|
| `team1_ec2_role` (기존) | `backend` | S3 `team1-truss-market-data`에 Get/PutObject, ListBucket — 완료됨, 손댈 필요 없음 |
| `team1_trader_role` (신규) | `trader` | S3 `team1-truss-order-records`에 PutObject + Bedrock `InvokeModel`(3번 참고) |
| `team1_replayengine_role` (신규) | `replayengine` | S3 `team1-truss-order-records`에 GetObject |
| `team1_recorder_role` (신규) | `recorder` | S3 `team1-truss-trade-results`에 PutObject |
| `orderapi`/`matching`/`recorder`용 역할 | `orderapi`, `matching`, `recorder` | S3/Bedrock 권한은 불필요하지만, **MSK Serverless 연결에 IAM 권한이 꼭 필요하다**(5번 참고) — `kafka-cluster:Connect`/`kafka-cluster:*Topic*`/`kafka-cluster:*Group*` 등을 클러스터 ARN으로 스코프해서 부여. Redis AUTH 토큰(선택)만 Secrets Manager 등에 저장해 환경변수로 주입 |

각 역할을 실제로 무엇에 붙일지(EC2 인스턴스 프로필 / EKS IRSA / Fargate 태스크 역할)는 컴퓨트를 뭘로 정하느냐에 따라 달라진다 — `backend`는 이미 EC2 인스턴스 프로필로 검증된 전례가 있음.

## 5. Kafka — 인증 지원 완료 (AWS_MSK_IAM, `infra/msk.tf`가 Serverless라 SASL/SCRAM은 옵션이 아님)

- 필요한 것: 브로커 엔드포인트 1개(`KAFKA_BROKER`), `orders`/`executions`/`assignments` 3개 토픽. **`orders`는 자동 생성에 맡기면 안 되고 정확히 20개 파티션으로 미리 만들어야 한다**(마켓 1개 = 파티션 1개 전제).
- **2026-08-10에는 SASL/SCRAM-SHA-512로 갔다가, 2026-08-11에 AWS_MSK_IAM으로 다시 바꿨다.** 이유는 인프라를 나중에 알게 됐기 때문이다 — `infra/msk.tf`가 실제로 만드는 건 MSK **Serverless**이고, AWS 공식 문서(MSK Serverless 안내 페이지)에 "MSK Serverless requires IAM access control for all clusters. Apache Kafka access control lists (ACLs) are not supported"라고 명시돼 있다. 즉 MSK Serverless에서는 SASL/SCRAM을 켤 방법이 아예 없다 — 인프라를 Provisioned로 바꿔서 기존 SCRAM 코드를 유지할 수도 있었지만, Serverless가 브로커 용량 계획/스케일링을 안 해도 되는 실질적 이점이 있어 **앱 코드를 IAM으로 바꾸는 쪽을 택했다.**
  - SASL/SCRAM을 골랐던 원래 근거("`segmentio/kafka-go`가 AWS_MSK_IAM을 지원 안 함")도 다시 확인해보니 불완전했다 — 메인 모듈 트리만 검색해서 놓친 것이고, 실제로는 `github.com/segmentio/kafka-go/sasl/aws_msk_iam_v2`라는 **별도 Go 서브모듈**로 2023년부터 존재한다(`go get`으로 독립적으로 받아야 해서 메인 모듈 검색에 안 걸렸던 것). AWS SDK v2 기반(`aws_msk_iam_v2.NewMechanism(awsCfg)`)이라 `trader`가 S3/Bedrock에 쓰는 것과 같은 자격증명 체인을 그대로 재사용한다 — 직접 SASL 프로토콜을 구현하는 것과는 리스크 성격이 다르다.
- **IAM 권한**: `orderapi`/`matching`/`recorder`가 쓰는 역할(EKS IRSA 등)에 `kafka-cluster:Connect`, `kafka-cluster:DescribeTopic`/`WriteData`/`ReadData`, `kafka-cluster:AlterGroup`/`DescribeGroup` 등 MSK IAM 정책 액션을 **클러스터 ARN으로 스코프**해서 부여해야 한다(계정 공유 환경이라 전체 리소스 아님, 이 클러스터만). 별도 사용자명/비밀번호나 Secrets Manager 저장이 필요 없다 — IAM 역할 자체가 자격증명이라 앱 쪽 환경변수는 `KAFKA_USE_IAM_AUTH=true` 하나뿐.
- **인증 없이 갈 수도 있다**: `KAFKA_USE_IAM_AUTH`를 비워두거나 `false`로 두면 인증 없는 연결(로컬 dev-kafka와 동일)로 동작한다 — 다만 **MSK Serverless를 쓰기로 한 이상 이 옵션은 실질적으로 없다**(IAM 필수). Provisioned로 바꾸면 다시 선택지가 생김.
- **아직 실 MSK로 검증 못함** — 로컬에서는 disposable SASL/SCRAM 브로커로 이전 구현을 검증했지만, `aws_msk_iam_v2` 자체는 실제 MSK Serverless 클러스터가 있어야 끝까지 검증할 수 있다. 클러스터가 뜨면 이 문서/CLAUDE.md에 검증 결과를 추가해야 함.

## 6. Redis — 인증 지원 완료 (AUTH 토큰 + TLS)

- 필요한 것: 엔드포인트 1개(`REDIS_ADDR`). `orderapi`(세션 가드, 백프레셔 체크, 호가창 캐시), `matching`(스냅샷, 백프레셔, 부하 추적), `recorder`(백프레셔 감시)가 전부 같은 인스턴스를 씀.
- ElastiCache를 **AUTH 토큰 있는 구성**으로 만들고 그 토큰을 `REDIS_PASSWORD`(세 모듈 공통)로 넘기면 됨 — `go-redis/v9`가 이미 지원하는 필드라 추가 구현 없이 바로 됨.
- **`infra/elasticache.tf`는 `transit_encryption_enabled=true`인데 `auth_token`은 안 둔다** — "TLS는 필수, 비밀번호는 없음" 조합이라, 2026-08-11에 별도로 `REDIS_TLS_ENABLED`를 추가했다(`REDIS_PASSWORD` 하나로는 이 조합을 표현 못 함). 지금 이 인프라 설정대로면 `REDIS_TLS_ENABLED=true`, `REDIS_PASSWORD`는 비워둠.
- 둘 다 비워두면 인증/TLS 없이 붙는다(로컬과 동일).

## 7. DB — MySQL, EC2 자체 호스팅 (RDS 아님, 2026-08-24 전환)

- 엔진: **MySQL 8.4**, 공식 `mysql` Docker 이미지로 EC2 인스턴스 위에서 직접 운영(`team1-mysql`, `i-09f1dca7e19bbd3ff`, `m6i.2xlarge`, private IP `10.10.10.178`, `eks_backend` 서브넷).
- `recorder`가 `DATABASE_URL`로 받는 값은 URL이 아니라 `go-sql-driver/mysql`의 DSN 형식: `user:pass@tcp(host:port)/dbname?parseTime=true&loc=UTC`.
- 스키마는 공식 `mysql` 이미지의 `/docker-entrypoint-initdb.d/` 최초 기동 메커니즘으로 자동 적용된다 — RDS 시절처럼 `recorder/schema.sql`을 손으로 최초 1회 적용할 필요가 없다.
- 자격증명은 `recorder-db-secret`이라는 K8s Secret(`DATABASE_URL` 키, 완성된 DSN)으로 전달 — Terraform 관리 대상이 아니라 RDS 시절과 동일하게 수동으로 생성/관리한다.
- 네트워크: 보안그룹 `team1_sg_mysql_ec2`가 `eks_backend` 보안그룹에서만 3306을 허용 — RDS 시절과 동일하게 퍼블릭 액세스 없음.
- **RDS(Multi-AZ DB 클러스터, `db.m6gd.large`)는 2026-08-21에 삭제됐다** — 부하테스트에서 recorder 쓰기량이 CPU를 지속적으로 99%까지 밀어붙이는 걸 실측했고, 이중 비용을 피하려고 기존 RDS부터 먼저 내린 뒤 이 EC2 구성으로 전환했다(상세는 `infra/rds.tf`의 주석 참고).

## 8. 네트워크 / 컴퓨트

컴퓨트는 EKS **단일 클러스터**로 확정·적용됨 — 역할별로 클러스터를 나누는 대신, 관리형 노드그룹(백엔드: 접수API·매칭엔진·기록기)과 Fargate Profile 3종(시세수집기/AI트레이더/리플레이, 네임스페이스로 격리)으로 워크로드만 분리한다(당초 역할별 3-클러스터 설계는 멘토링에서 반려됨). 이 문서가 정리한 환경변수/IAM/S3/Bedrock 요구사항 자체는 이 결정과 무관하게 동일하게 적용된다.
- 이미지 베이스: Go 바이너리라 정적 컴파일된 단일 실행 파일 배포가 가능함(Python처럼 런타임+의존성 설치가 필요 없음) — 컨테이너화한다면 `scratch`/`alpine` 같은 매우 가벼운 베이스로 충분.

## 9. 아직 정해지지 않은 것 (확인 후 알려주면 반영)

- Bedrock 모델의 `ap-northeast-2` 가용 여부 / 크로스 리전 프로파일 필요 여부
- Redis를 인증 없이(보안 그룹만) 갈지 AUTH+TLS를 쓸지는 인프라 쪽 판단(코드는 이미 둘 다 지원). **Kafka는 MSK Serverless를 쓰는 이상 IAM 인증이 필수**(선택지 아님) — `orderapi`/`matching`/`recorder`가 쓰는 역할에 MSK IAM 정책만 붙이면 됨
- AWS_MSK_IAM(`aws_msk_iam_v2`)을 실제 MSK Serverless 클러스터로 끝까지 검증하는 것 — 클러스터가 뜨는 대로 진행 필요
- 컴퓨트 방식(EKS/EC2/Lambda+K8s Job) 최종 결정
