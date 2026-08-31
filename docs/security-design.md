# 보안 및 권한 설계서

## 문서 개요
이 문서는 Truss 프로젝트에 실제로 적용된 보안·권한 설계를 정리한 것이다. 별도의 설계 산출물 없이 `infra/` 아래 Terraform·Kubernetes 매니페스트에 코드+주석 형태로 흩어져 있던 내용을 한 문서로 모았으며, 각 항목마다 근거가 된 실제 파일 경로를 표기해 검증 가능하게 했다. `requirements.md` NFR-18("비밀번호·접속정보를 설정 파일에 적어두지 않고, 서버끼리 꼭 필요한 통신만 허용하며, 컨테이너를 관리자 권한으로 실행하지 않음")의 상세 구현 근거 문서 역할을 겸한다.

이 프로젝트는 여러 팀이 공유하는 단일 AWS 계정(`727646470302`) 위에서 동작한다는 제약이 설계 전반에 깔려 있다 — 모든 IAM 정책은 리소스 ARN 단위로 좁혀서 부여하고, 리소스 이름은 `team1-*`로 네임스페이싱한다.

## 1. 설계 원칙
1. **최소 권한(least privilege)** — 서비스마다 정확히 필요한 액션·리소스만 부여한다. 예를 들어 시세 수집기는 S3만, AI 트레이더는 S3+Bedrock만 필요하고 Kafka 권한 자체가 없다.
2. **신원의 3단 분리** — 사람(Terraform을 실행하는 공유 학생 계정), 애플리케이션(파드별 IRSA 역할), 자동화(Job 트리거 Lambda, GitHub Actions CI)를 각각 별도의 IAM 주체로 둔다. 하나가 뚫려도 다른 두 경로의 권한을 그대로 얻지 못한다.
3. **네트워크 계층 + IAM 계층 이중 격리** — 같은 격리를 보안 그룹과 IAM 양쪽에서 각각 건다. 예를 들어 Kafka를 쓰지 않는 collector/ai-trader Fargate 파드는 IRSA에도 MSK 권한이 없고, 보안 그룹으로도 MSK 인그레스 자체를 backend 노드그룹으로만 제한해 둘 중 하나만 뚫려도 막힌다.
4. **비밀정보는 설정 파일에 두지 않는다** — DB 접속정보는 AWS Secrets Manager에서 K8s Secret으로 동기화하고, Terraform 코드에도 평문으로 적지 않는다.
5. **자동화 권한은 "만들고 지우는" 데까지만** — CI/CD나 Job 트리거 Lambda 같은 자동화 주체에는 리소스 생성·조회·삭제까지만 주고, 이보다 민감한 리소스(Ingress, RBAC, Secret 자체)는 사람이 직접 적용한다.

## 2. IAM — 서비스별 최소권한 (IRSA)
단일 EKS 클러스터, OIDC 프로바이더 1개를 모든 서비스어카운트가 공유한다(`infra/irsa.tf`). 서비스어카운트는 K8s Deployment/Job이 실제로 assume하는 단위이며, `eks.amazonaws.com/role-arn` 애너테이션(`infra/k8s/*/serviceaccount.yaml`)으로 IAM 역할과 1:1 매핑된다.

| 서비스어카운트 | 네임스페이스 | 부여된 권한 | 의도적으로 주지 않은 권한 |
|---|---|---|---|
| `sa-ingest-api` (접수 API) | backend | Kafka `orders` 발행, `executions` 구독(체결 결과 되읽기), MSK 컨슈머 그룹 제어, Job 트리거 SQS 발행, S3 `order-records` 읽기(`GET /v1/jobs/replay-preview`용) | K8s Job 생성 RBAC(공격 표면 축소 목적 — SQS+Lambda 경로로 우회), S3 쓰기 |
| `sa-matching-engine` (매칭 엔진) | backend | Kafka `orders`/`executions`/`assignments` 구독·발행, 컨슈머 그룹 제어 | S3, Bedrock 전부 불필요 — 부여 안 함 |
| `sa-recorder` (기록기) | backend | Kafka `orders`/`executions`/`assignments` 구독, S3 `trade-results` 쓰기, Secrets Manager에서 DB 접속정보 읽기(`secretsmanager:GetSecretValue`/`DescribeSecret`, 해당 시크릿 ARN으로만 스코프) | Kafka 발행 권한(읽기 전용 소비자) |
| `sa-collector` (시세 수집기) | collector | S3 `market-data` 읽기+쓰기(HeadObject 캐시 히트 시 직접 서빙하므로 읽기도 필요) | Kafka 전면 미부여 — 시세 데이터는 Kafka를 쓰지 않기로 한 설계 결정과 일치 |
| `sa-ai-trader` (AI 트레이더) | ai-trader | S3 `market-data` 읽기, S3 `order-records` 읽기+쓰기(기존 파일과 병합 후 재저장하는 구조라 읽기도 필요), Bedrock `InvokeModel`(특정 추론 프로파일 ARN + 그 프로파일이 라우팅하는 APAC 5개 리전의 파운데이션 모델 ARN으로만 스코프) | Kafka, RDS/MySQL 전부 미부여 — 주문 제출은 HTTP로 접수 API를 호출해서 함 |
| `sa-replay-engine` (리플레이 엔진) | replay | S3 `order-records` 읽기 전용 | Kafka, RDS 미부여(위와 같은 이유) |

세부 정책 문서는 `infra/irsa.tf` — 각 정책 블록에 실제 운영 중 발견된 권한 누락 사례(예: WAF가 아니라 실제 구동 로그로 발견한 `assignments` 토픽 구독 누락, `order-records` GetObject 누락으로 인한 저장 실패 등)가 주석으로 함께 남아 있다.

## 3. Kubernetes RBAC — 클러스터 내부 권한
IRSA가 "AWS API를 부를 수 있는가"를 통제한다면, K8s RBAC은 "K8s API 자체를 조작할 수 있는가"를 통제한다. 이 프로젝트에는 사람이 아닌 세 자동화 주체가 K8s API를 직접 호출하며, 각각 최소 범위로 좁혀져 있다(`infra/k8s/job-trigger-rbac.yaml`, `ci-deploy-rbac.yaml`, `backend/orderapi-matching-restart-rbac.yaml`).

| 주체 | 매핑 방식 | 부여된 권한 | 범위 |
|---|---|---|---|
| Job 트리거 Lambda | EKS Access Entry → `kubernetes_groups: ["team1-job-trigger-lambda"]`(`infra/job-trigger.tf`) | `batch/jobs`에 create/get/list/watch/delete, `pods`/`pods/log` 조회 | `ai-trader`/`replay` 네임스페이스만. update/patch는 없음 — "만들고 상태 보고 취소되면 지우는" 것까지만 |
| GitHub Actions CI/CD | EKS Access Entry → `kubernetes_groups: ["team1-github-actions-deploy"]`(`infra/cicd.tf`) | `deployments`/`replicasets`/`services`에 get/list/watch/create/patch/update, `pods` 조회 | `backend`/`collector` 네임스페이스. Ingress·RBAC·Secret 등 민감 리소스는 제외 — 여전히 사람이 직접 적용 |
| orderapi 자기 자신 (파드 내부, `sa-ingest-api` 서비스어카운트) | RoleBinding(K8s RBAC) — IRSA와 무관한 별도 인가 경로 | `deployments/matching-engine`에 get/patch(재시작 트리거)만, `deployments/orderapi,recorder`에 get만 | `resourceNames`로 이름까지 못박아 다른 Deployment는 애초에 건드릴 수 없게 RBAC 레벨에서 차단 |

GitHub Actions는 OIDC 연동(아래 8절)으로 액세스 키 없이 역할을 assume하며, `main`/`prod` 브랜치에서 실행되는 워크플로로 트러스트 정책의 `sub` 조건이 제한돼 있다.

## 4. 네트워크 보안 그룹 (`infra/network/security-groups.tf`)
Fargate 파드는 프로파일별 개별 보안 그룹을 가질 수 없어(Security Groups for Pods는 EC2/Nitro 노드 전용) 클러스터 SG를 공유하지만, MSK/Redis/MySQL처럼 격리가 중요한 리소스는 인그레스 자체를 특정 SG로 제한해 IAM과 별개로 네트워크 경로를 차단한다.

| 보안 그룹 | 허용 인그레스 | 효과 |
|---|---|---|
| `team1_sg_msk` | backend 노드그룹 SG에서 9098(Kafka IAM/SASL)만 | Kafka를 쓰지 않는 collector/ai-trader/replay(Fargate)는 네트워크 경로 자체가 없음 — IRSA 미부여와 이중 방어 |
| `team1_sg_redis` | backend 노드그룹 SG에서 6379만 | 동일한 이유로 Redis도 backend 외 경로 차단 |
| `team1_sg_mysql_ec2` | backend 노드그룹 SG에서 3306만 (`infra/mysql-ec2.tf`) | RDS에서 자체 호스팅 MySQL(EC2)로 전환된 뒤에도 퍼블릭 액세스 없음 유지 |
| `team1_sg_alb_public` | 인터넷에서 443/80만 | 접수 API·시세 수집기·기록기 조회 API의 유일한 외부 진입점. 각 백엔드 포트(8081/8080/8082)로의 인그레스는 이 ALB SG에서 오는 트래픽만 개별 허용 |
| `team1_sg_lambda_job_trigger` | 없음(항상 발신 쪽) | Job 트리거 Lambda는 VPC에 붙어 SQS를 소비하고 EKS 프라이빗 엔드포인트만 호출 |
| `team1_sg_vpc_endpoints` | backend/EKS 클러스터/Lambda SG + VPC CIDR에서 443만 | ECR·CloudWatch Logs·STS·Bedrock·SQS Interface 엔드포인트 접근을 인터넷 경유 없이 처리 |

## 5. 시크릿 관리 — AWS Secrets Manager + Secrets Store CSI Driver
recorder의 `DATABASE_URL`(MySQL 접속정보, 비밀번호 포함)을 코드나 K8s 매니페스트에 평문으로 두지 않기 위한 흐름:

1. `infra/secrets-manager.tf` — `team1/backend/mysql-db-url`이라는 Secrets Manager 시크릿에 완성된 DSN을 저장(값 자체는 `random_password`로 생성된 MySQL root 비밀번호를 참조하며, 코드에 하드코딩된 비밀번호는 없음).
2. `infra/csi-secrets-store.tf` — Secrets Store CSI Driver(Helm)를 `kube-system`에 설치.
3. `infra/k8s/backend/recorder-db-secret-provider.yaml` — `SecretProviderClass`가 위 시크릿을 `jmesPath`로 읽어 `recorder-db-secret`이라는 K8s Secret으로 동기화. recorder 파드는 볼륨 마운트로 이 `SecretProviderClass`를 참조해야 동기화가 트리거된다.
4. `recorder-deployment.yaml`은 여전히 `recorder-db-secret`을 `secretKeyRef`로 참조 — Secret의 "생성자"만 수동 생성에서 CSI 동기화로 바뀐 것이라 애플리케이션 코드/설정은 변경이 필요 없다.

접근 권한은 `sa-recorder` IRSA 역할에 해당 시크릿 ARN 하나로 스코프된 `secretsmanager:GetSecretValue`/`DescribeSecret`만 부여돼 있다(2절 참고) — CSI 드라이버 자체는 별도 IRSA 없이 마운트하는 파드의 신원을 그대로 쓰는 구조다.

## 6. 전송 구간 인증·암호화
| 구간 | 방식 | 비고 |
|---|---|---|
| 애플리케이션 ↔ Kafka(MSK Serverless) | AWS_MSK_IAM(필수, 선택 불가) | MSK Serverless는 SASL/SCRAM·ACL을 지원하지 않고 IAM 인증만 지원 — AWS 공식 문서로 확인 후 설계를 SCRAM에서 IAM으로 전환. IAM 역할 자체가 자격증명이라 별도 사용자명/비밀번호가 없음(`KAFKA_USE_IAM_AUTH=true`) |
| 애플리케이션 ↔ Redis(ElastiCache) | `transit_encryption_enabled=true`(TLS 필수) + AUTH 토큰 없음 | `REDIS_TLS_ENABLED`/`REDIS_PASSWORD`를 독립된 환경변수로 분리해 "TLS는 필수, 비밀번호는 없음" 조합을 표현 |
| 외부 사용자 → 프론트엔드 | CloudFront + ACM 인증서(TLS 1.2 이상) + OAC(S3는 퍼블릭 액세스 계속 차단, CloudFront를 통해서만 접근) | |
| 외부 사용자 → 접수 API/기록기 조회 API/시세 수집기 | CloudFront → Public ALB(HTTPS 443, ACM 인증서) → 각 백엔드 포트 | ALB에는 WAF(AWS 관리형 규칙 + IP당 rate limit)가 연결됨(아래 참고) |

**WAF (`infra/edge.tf`)**: Public ALB에 `AWSManagedRulesCommonRuleSet`, `AWSManagedRulesKnownBadInputsRuleSet`, IP당 rate limit(20,000/5분, 부하 시험 트래픽을 막지 않도록 여유 있게 설정한 값) 세 규칙을 연결.

## 7. 저장소(S3) 접근 통제
- 모든 버킷(`team1-truss-market-data`, `team1-truss-trade-results`, `team1-truss-order-records`, 프론트엔드용 버킷)에 SSE-S3(AES256) 암호화와 퍼블릭 액세스 전면 차단을 적용.
- 프론트엔드 버킷만 예외적으로 CloudFront OAC를 통한 접근을 허용 — 버킷 정책이 `cloudfront.amazonaws.com` 프린시펄을 `AWS:SourceArn` 조건(이 CloudFront 배포로 한정)으로만 허용한다(`infra/edge.tf`의 `frontend_oac`).
- 버킷별 쓰기/읽기 권한은 2절 IRSA 표에 정리된 대로 서비스어카운트 단위로 분리돼 있고, 어떤 서비스도 자신이 쓰지 않는 버킷에는 권한이 없다.
- 라이프사이클 규칙 없음 — 인프라 변경 전후 동일한 데이터로 성능을 비교해야 하므로 의도적으로 만료/전환을 걸지 않음(보안이 아니라 프로젝트 요구사항에 따른 결정이지만, 데이터 보존 정책 관점에서 함께 기록).

## 8. CI/CD 파이프라인 신원 (`infra/cicd.tf`)
GitHub Actions는 액세스 키를 저장하지 않고 GitHub OIDC 토큰으로 IAM 역할(`team1-github-actions-ecr-push`)을 assume한다.

- **트러스트 조건**: `main`/`prod` 브랜치에서 실행되는 워크플로만 허용(`sub` 클레임 조건). GitHub의 sub 클레임 포맷 변경(owner/repo에 불변 숫자 ID가 붙는 방식)에 대응해 신·구 포맷을 모두 등록해 둠.
- **부여 권한**: ECR push(해당 리포지토리 ARN으로 스코프), Job 트리거 Lambda 코드 갱신, 프론트엔드 S3 버킷 동기화 + CloudFront invalidation(해당 버킷/배포 ARN으로 스코프), `eks:DescribeCluster`(kubectl 인증용 — 실제 K8s 권한은 3절의 access entry+RBAC이 별도로 통제).
- **`cloudfront:ListDistributions`만 예외적으로 `Resource: *`** — IAM에서 리소스 단위로 제한 불가능한 액션이라 읽기 전용으로만 허용(다른 팀 리소스 목록 조회는 가능하나 변경은 불가).

## 9. 사람 계정과 런타임 신원의 분리
- **Terraform 실행 주체**: 공유 학생 IAM 사용자(`a-student-05`) — 계정 전체에 넓은 권한을 갖지만, 이는 인프라를 프로비저닝하는 사람의 신원이지 애플리케이션이 런타임에 사용하는 자격증명이 아니다.
- **애플리케이션 런타임 신원**: 전부 IRSA(2절) — 파드가 AWS API를 호출할 때 이 서비스어카운트 역할만 사용하며, Terraform 실행자의 권한을 물려받지 않는다.
- **EKS 클러스터 관리자 접근**: 별도의 EKS Access Entry로 관리(예: `a-student-13`)되며, 이 역시 애플리케이션 IRSA와는 분리된 경로다.
- **DB 접속**: MySQL은 마스터 유저를 쓰되 그 비밀번호 자체를 Secrets Manager에서 관리(5절)하므로, "누가 이 시크릿을 읽을 IAM 권한이 있는가"만 통제하면 되고 K8s 레벨에서 DB 계정을 별도 발급하지 않는다.

## 10. 컨테이너 보안
6개 Go 백엔드 모듈(`backend`/`trader`/`orderapi`/`matching`/`replayengine`/`recorder`) 모두 동일한 Dockerfile 패턴을 따른다:
- 멀티스테이지 빌드(`golang:1.26-alpine` 빌드 → `alpine:3.20`+`ca-certificates` 런타임)로 빌드 도구를 최종 이미지에 남기지 않음.
- `CGO_ENABLED=0` 정적 바이너리.
- **non-root 사용자로 실행**(NFR-18의 "컨테이너를 관리자 권한으로 실행하지 않음" 항목에 직접 대응).
- `.dockerignore`로 각 모듈의 gitignore된 `.env`와 로컬 폴백 저장 디렉터리를 이미지 빌드 컨텍스트에서 제외 — 개발 머신의 비밀정보나 로컬 임시 파일이 이미지에 실수로 포함되는 경로를 원천 차단.

## 11. 알려진 한계 / 후속 확인 필요 사항
- **AWS_MSK_IAM 경로는 실제 MSK Serverless 클러스터로 끝까지 검증되지 않았다** — 로컬 무인증 경로(`KAFKA_USE_IAM_AUTH=false`)는 라이브 검증됐지만, `useIAM=true` 경로는 코드/SDK 문서 대조로 작성했을 뿐 VPC 내부에서 실제 IAM 인증 접속을 확인한 기록은 아직 없다.
- **Redis AUTH 토큰은 이번 설계에서 비워두는 쪽으로 결정**(`transit_encryption_enabled=true`만 적용, `auth_token` 없음) — TLS는 강제되지만 비밀번호 인증까지는 켜지 않은 상태이며, 필요 시 `REDIS_PASSWORD`를 채우고 Secrets Manager 경로로 주입하도록 확장 가능하다.
- **공유 계정 제약** — Terraform을 실행하는 학생 계정과 애플리케이션 런타임 신원(IRSA)이 분리돼 있긴 하지만, 계정 자체는 여러 팀이 공유하므로 `cloudfront:ListDistributions`처럼 IAM으로 리소스 단위 제한이 불가능한 일부 액션은 읽기 전용으로만 허용해 위험을 낮췄다.
- **RBAC 세분화는 자동화 주체 3곳(Job 트리거 Lambda, GitHub Actions, orderapi 자기 자신)에 한정** — 사람이 직접 `kubectl`로 접근하는 경로(EKS 클러스터 관리자)의 세부 RBAC 정책은 이 문서의 범위 밖이며 별도 확인이 필요하다.
