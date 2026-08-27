# 서비스 어카운트별 IRSA. 단일 클러스터라 OIDC 프로바이더 1개를 전부 공유한다. 네임스페이스는
# k8s/*.yaml과 동일(backend/collector/ai-trader/replay). 시세수집기·AI트레이더는 Kafka 대신
# S3로 직접 주고받아 MSK 권한이 없다 — Kafka는 backend(접수API·매칭엔진·기록기)만 접속한다.

locals {
  msk_topic_arn_prefix = replace(aws_msk_serverless_cluster.team1_truss.arn, ":cluster/", ":topic/")
  msk_group_arn_prefix = replace(aws_msk_serverless_cluster.team1_truss.arn, ":cluster/", ":group/")
}

# ---------------------------------------------------------------------------
# sa-ingest-api (backend) — MSK 발행(orders), Job 트리거 SQS 발행(job-trigger.tf)
# Job 생성 RBAC은 여기 부여하지 않는다 — 공격 표면 축소가 SQS+Lambda 경로를 쓰는 이유.

data "aws_iam_policy_document" "irsa_ingest_api_assume" {
  statement {
    actions = ["sts:AssumeRoleWithWebIdentity"]
    principals {
      type        = "Federated"
      identifiers = [aws_iam_openid_connect_provider.team1_eks.arn]
    }
    condition {
      test     = "StringEquals"
      variable = "${local.eks_oidc_url_no_scheme}:sub"
      values   = ["system:serviceaccount:backend:sa-ingest-api"]
    }
    condition {
      test     = "StringEquals"
      variable = "${local.eks_oidc_url_no_scheme}:aud"
      values   = ["sts.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "sa_ingest_api" {
  name               = "team1-sa-ingest-api"
  assume_role_policy = data.aws_iam_policy_document.irsa_ingest_api_assume.json
  tags               = { Team = "team1", Name = "team1-sa-ingest-api" }
}

data "aws_iam_policy_document" "sa_ingest_api_policy" {
  statement {
    # WriteDataIdempotently — Kafka 3.0+ 클라이언트는 기본으로 producer idempotence를 켜는데,
    # 이 경우 MSK IAM은 토픽 단위 WriteData와 별개로 클러스터 단위 이 권한을 요구한다
    # (실제로 없으면 ClusterAuthorizationException, 클라이언트 쪽엔 EOF로 나타남 — 실행해보고 발견).
    actions   = ["kafka-cluster:Connect", "kafka-cluster:WriteDataIdempotently"]
    resources = [aws_msk_serverless_cluster.team1_truss.arn]
  }
  statement {
    actions   = ["kafka-cluster:WriteData", "kafka-cluster:DescribeTopic"]
    resources = ["${local.msk_topic_arn_prefix}/orders"]
  }
  statement {
    # executions — 체결 결과를 접수 API가 되읽어 응답에 반영하는 걸 실제 구동 로그로 발견
    # (문서엔 "발행만"으로 적혀 있었으나 컨슈머 그룹도 붙는다).
    actions   = ["kafka-cluster:ReadData", "kafka-cluster:DescribeTopic"]
    resources = ["${local.msk_topic_arn_prefix}/executions"]
  }
  statement {
    actions   = ["kafka-cluster:AlterGroup", "kafka-cluster:DescribeGroup"]
    resources = ["${local.msk_group_arn_prefix}/*"]
  }
  statement {
    actions   = ["sqs:SendMessage"]
    resources = [aws_sqs_queue.job_trigger.arn]
  }
  # GET /v1/jobs/replay-preview(orderapi/replaypreview.go)가 trader가 남긴 주문 기록을
  # 읽는다 — sa-replay-engine과 같은 읽기 전용 권한(2026-08-20, startJobHandler가
  # orderBucket 기본값을 채우도록 고치면서 같이 발견: 이 권한이 원래 빠져 있어서
  # ORDER_RECORDS_BUCKET을 채워도 이 엔드포인트는 AccessDenied가 났을 것).
  statement {
    actions   = ["s3:GetObject"]
    resources = ["${aws_s3_bucket.order_records.arn}/*"]
  }
  statement {
    actions   = ["s3:ListBucket"]
    resources = [aws_s3_bucket.order_records.arn]
  }
}

resource "aws_iam_role_policy" "sa_ingest_api" {
  name   = "team1-sa-ingest-api-policy"
  role   = aws_iam_role.sa_ingest_api.id
  policy = data.aws_iam_policy_document.sa_ingest_api_policy.json
}

# ---------------------------------------------------------------------------
# sa-matching-engine (backend) — MSK 구독/발행(orders, executions)

data "aws_iam_policy_document" "irsa_matching_engine_assume" {
  statement {
    actions = ["sts:AssumeRoleWithWebIdentity"]
    principals {
      type        = "Federated"
      identifiers = [aws_iam_openid_connect_provider.team1_eks.arn]
    }
    condition {
      test     = "StringEquals"
      variable = "${local.eks_oidc_url_no_scheme}:sub"
      values   = ["system:serviceaccount:backend:sa-matching-engine"]
    }
    condition {
      test     = "StringEquals"
      variable = "${local.eks_oidc_url_no_scheme}:aud"
      values   = ["sts.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "sa_matching_engine" {
  name               = "team1-sa-matching-engine"
  assume_role_policy = data.aws_iam_policy_document.irsa_matching_engine_assume.json
  tags               = { Team = "team1", Name = "team1-sa-matching-engine" }
}

data "aws_iam_policy_document" "sa_matching_engine_policy" {
  statement {
    # WriteDataIdempotently — 위 sa_ingest_api와 동일한 이유 (Kafka 3.0+ 기본 idempotent producer).
    actions   = ["kafka-cluster:Connect", "kafka-cluster:WriteDataIdempotently"]
    resources = [aws_msk_serverless_cluster.team1_truss.arn]
  }
  statement {
    actions = ["kafka-cluster:ReadData", "kafka-cluster:WriteData", "kafka-cluster:DescribeTopic"]
    resources = [
      "${local.msk_topic_arn_prefix}/orders",
      "${local.msk_topic_arn_prefix}/executions",
      # assignments — 마켓 재분배 조율용, 매칭 엔진 실제 구동 로그로 확인된 세 번째 토픽
      # (문서엔 없었음, 실행해보고 발견).
      "${local.msk_topic_arn_prefix}/assignments",
    ]
  }
  statement {
    actions   = ["kafka-cluster:AlterGroup", "kafka-cluster:DescribeGroup"]
    resources = ["${local.msk_group_arn_prefix}/*"]
  }
}

resource "aws_iam_role_policy" "sa_matching_engine" {
  name   = "team1-sa-matching-engine-policy"
  role   = aws_iam_role.sa_matching_engine.id
  policy = data.aws_iam_policy_document.sa_matching_engine_policy.json
}

# ---------------------------------------------------------------------------
# sa-recorder (backend) — MSK 구독(executions), RDS 접속(Secrets Manager), S3 PutObject
# (trade-results). RDS는 마스터 유저를 쓰므로 IRSA엔 시크릿 읽기 권한만 부여.

data "aws_iam_policy_document" "irsa_recorder_assume" {
  statement {
    actions = ["sts:AssumeRoleWithWebIdentity"]
    principals {
      type        = "Federated"
      identifiers = [aws_iam_openid_connect_provider.team1_eks.arn]
    }
    condition {
      test     = "StringEquals"
      variable = "${local.eks_oidc_url_no_scheme}:sub"
      values   = ["system:serviceaccount:backend:sa-recorder"]
    }
    condition {
      test     = "StringEquals"
      variable = "${local.eks_oidc_url_no_scheme}:aud"
      values   = ["sts.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "sa_recorder" {
  name               = "team1-sa-recorder"
  assume_role_policy = data.aws_iam_policy_document.irsa_recorder_assume.json
  tags               = { Team = "team1", Name = "team1-sa-recorder" }
}

data "aws_iam_policy_document" "sa_recorder_policy" {
  statement {
    actions   = ["kafka-cluster:Connect"]
    resources = [aws_msk_serverless_cluster.team1_truss.arn]
  }
  statement {
    actions = ["kafka-cluster:ReadData", "kafka-cluster:DescribeTopic"]
    resources = [
      "${local.msk_topic_arn_prefix}/executions",
      # orders, assignments — recorder 실제 구동 로그로 확인됨 (executions 하나로 부족,
      # 세 토픽 모두 구독한다).
      "${local.msk_topic_arn_prefix}/orders",
      "${local.msk_topic_arn_prefix}/assignments",
    ]
  }
  statement {
    actions   = ["kafka-cluster:AlterGroup", "kafka-cluster:DescribeGroup"]
    resources = ["${local.msk_group_arn_prefix}/*"]
  }
  statement {
    actions   = ["s3:PutObject"]
    resources = ["${aws_s3_bucket.trade_results.arn}/*"]
  }
  statement {
    actions   = ["s3:ListBucket"]
    resources = [aws_s3_bucket.trade_results.arn]
  }
}

resource "aws_iam_role_policy" "sa_recorder" {
  name   = "team1-sa-recorder-policy"
  role   = aws_iam_role.sa_recorder.id
  policy = data.aws_iam_policy_document.sa_recorder_policy.json
}

# 2026-08-27: Secrets Store CSI Driver(csi-secrets-store.tf)가 recorder 파드 자신의
# IRSA(이 역할)를 통해 team1/backend/mysql-db-url(secrets-manager.tf)을 읽어온다 —
# 드라이버/프로바이더 자체는 별도 IRSA가 없고(csi-secrets-store-provider-aws SA에
# role-arn 애너테이션 없음, 실측 확인), 마운트하는 파드의 신원을 그대로 쓰는 구조라
# 별도 역할이 아니라 기존 sa-recorder 역할에 인라인 정책만 추가한다.
data "aws_iam_policy_document" "sa_recorder_secretsmanager" {
  statement {
    actions   = ["secretsmanager:GetSecretValue", "secretsmanager:DescribeSecret"]
    resources = [aws_secretsmanager_secret.recorder_mysql_db_url.arn]
  }
}

resource "aws_iam_role_policy" "sa_recorder_secretsmanager" {
  name   = "team1-recorder-secretsmanager-read"
  role   = aws_iam_role.sa_recorder.id
  policy = data.aws_iam_policy_document.sa_recorder_secretsmanager.json
}

# ---------------------------------------------------------------------------
# sa-collector (시세 수집기, collector ns) — S3 PutObject(market-data)만. Kafka 미사용.

data "aws_iam_policy_document" "irsa_collector_assume" {
  statement {
    actions = ["sts:AssumeRoleWithWebIdentity"]
    principals {
      type        = "Federated"
      identifiers = [aws_iam_openid_connect_provider.team1_eks.arn]
    }
    condition {
      test     = "StringEquals"
      variable = "${local.eks_oidc_url_no_scheme}:sub"
      values   = ["system:serviceaccount:collector:sa-collector"]
    }
    condition {
      test     = "StringEquals"
      variable = "${local.eks_oidc_url_no_scheme}:aud"
      values   = ["sts.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "sa_collector" {
  name               = "team1-sa-collector"
  assume_role_policy = data.aws_iam_policy_document.irsa_collector_assume.json
  tags               = { Team = "team1", Name = "team1-sa-collector" }
}

data "aws_iam_policy_document" "sa_collector_policy" {
  statement {
    # GetObject — backend/server.go의 fileHandler가 캐시 히트 시 직접 읽어서
    # 서빙한다(HeadObject 존재 확인 후 미스일 때만 새로 수집). PutObject만 주고
    # GetObject를 안 줬더니 이미 캐시된 데이터 조회조차 403으로 실패하는 걸
    # 라이브로 확인했다 — collector가 쓰기 전용이라는 원래 가정이 실제 구현과
    # 안 맞았다.
    actions   = ["s3:GetObject", "s3:PutObject"]
    resources = ["${aws_s3_bucket.market_data.arn}/*"]
  }
  statement {
    actions   = ["s3:ListBucket"]
    resources = [aws_s3_bucket.market_data.arn]
  }
}

resource "aws_iam_role_policy" "sa_collector" {
  name   = "team1-sa-collector-policy"
  role   = aws_iam_role.sa_collector.id
  policy = data.aws_iam_policy_document.sa_collector_policy.json
}

# ---------------------------------------------------------------------------
# sa-ai-trader (AI 트레이더, ai-trader ns) — S3 GetObject(market-data 읽기), S3
# PutObject(order-records 쓰기), Bedrock InvokeModel(LLM 쓰는 봇만 해당하므로 이 역할에만 부여).

data "aws_iam_policy_document" "irsa_aitrader_assume" {
  statement {
    actions = ["sts:AssumeRoleWithWebIdentity"]
    principals {
      type        = "Federated"
      identifiers = [aws_iam_openid_connect_provider.team1_eks.arn]
    }
    condition {
      test     = "StringEquals"
      variable = "${local.eks_oidc_url_no_scheme}:sub"
      values   = ["system:serviceaccount:ai-trader:sa-ai-trader"]
    }
    condition {
      test     = "StringEquals"
      variable = "${local.eks_oidc_url_no_scheme}:aud"
      values   = ["sts.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "sa_ai_trader" {
  name               = "team1-sa-ai-trader"
  assume_role_policy = data.aws_iam_policy_document.irsa_aitrader_assume.json
  tags               = { Team = "team1", Name = "team1-sa-ai-trader" }
}

data "aws_iam_policy_document" "sa_ai_trader_policy" {
  statement {
    actions   = ["s3:GetObject"]
    resources = ["${aws_s3_bucket.market_data.arn}/*"]
  }
  statement {
    actions   = ["s3:ListBucket"]
    resources = [aws_s3_bucket.market_data.arn]
  }
  statement {
    # 2026-08-25 실측: trader가 주문 기록을 저장할 때(orderstore/s3.go) 기존 파일을
    # 먼저 GetObject로 읽어 병합한 뒤 PutObject로 다시 쓴다 — PutObject만 부여돼
    # 있어서 GetObject가 계속 AccessDenied로 실패, 거의 모든 마켓에서 주문 기록
    # 저장이 실패하고 있었다("PutObject(order-records 쓰기)"라고만 의도했던 게
    # 실제로는 읽기도 필요했던 것). 80배속 세션 로그에서 라이브로 발견.
    actions   = ["s3:GetObject", "s3:PutObject"]
    resources = ["${aws_s3_bucket.order_records.arn}/*"]
  }
  statement {
    actions   = ["s3:ListBucket"]
    resources = [aws_s3_bucket.order_records.arn]
  }
}

resource "aws_iam_role_policy" "sa_ai_trader" {
  name   = "team1-sa-ai-trader-policy"
  role   = aws_iam_role.sa_ai_trader.id
  policy = data.aws_iam_policy_document.sa_ai_trader_policy.json
}

variable "bedrock_model_region" {
  description = "Bedrock 모델을 실제로 호출하는 리전 — ap-northeast-2에서 실제 InvokeModel 성공 확인함(2026-08-12), 크로스 리전 프로파일 불필요"
  type        = string
  default     = "ap-northeast-2"
}

# 2026-08-12에 실측: anthropic.claude-haiku-4-5-20251001-v1:0을 raw model ID로 바로
# 호출하면 "on-demand throughput isn't supported, use an inference profile"로 거부됨
# — 이 계정에서 쓸 수 있는 최신 모델들은 온디맨드 직접 호출이 아니라 추론 프로파일을
# 통해서만 호출 가능하다. apac.anthropic.claude-3-haiku-20240307-v1:0(APAC 추론
# 프로파일)로 실제 InvokeModel 성공 확인함 — "가장 저렴한 모델" 팀 결정에 부합.
# 추론 프로파일은 등록된 여러 리전의 파운데이션 모델로 라우팅되므로(aws bedrock
# get-inference-profile로 실제 확인한 5개 APAC 리전), 프로파일 ARN뿐 아니라 그
# 리전들의 foundation-model ARN에도 권한이 있어야 한다.
locals {
  bedrock_inference_profile_id = "apac.anthropic.claude-3-haiku-20240307-v1:0"
  bedrock_underlying_model_id  = "anthropic.claude-3-haiku-20240307-v1:0"
  bedrock_apac_regions = [
    "ap-northeast-1", "ap-northeast-2", "ap-southeast-1", "ap-southeast-2", "ap-south-1",
  ]
}

data "aws_iam_policy_document" "sa_ai_trader_bedrock_policy" {
  statement {
    actions   = ["bedrock:InvokeModel"]
    resources = ["arn:aws:bedrock:${var.bedrock_model_region}:${data.aws_caller_identity.current.account_id}:inference-profile/${local.bedrock_inference_profile_id}"]
  }
  statement {
    actions = ["bedrock:InvokeModel"]
    resources = [
      for region in local.bedrock_apac_regions :
      "arn:aws:bedrock:${region}::foundation-model/${local.bedrock_underlying_model_id}"
    ]
  }
}

resource "aws_iam_role_policy" "sa_ai_trader_bedrock" {
  name   = "team1-sa-ai-trader-bedrock-policy"
  role   = aws_iam_role.sa_ai_trader.id
  policy = data.aws_iam_policy_document.sa_ai_trader_bedrock_policy.json
}

# ---------------------------------------------------------------------------
# sa-replay-engine (리플레이, replay ns) — S3 GetObject(order-records 읽기 전용). 주문
# 제출은 접수 API(HTTP)를 호출해서 하므로 Kafka/RDS 권한은 필요 없다.

data "aws_iam_policy_document" "irsa_replay_assume" {
  statement {
    actions = ["sts:AssumeRoleWithWebIdentity"]
    principals {
      type        = "Federated"
      identifiers = [aws_iam_openid_connect_provider.team1_eks.arn]
    }
    condition {
      test     = "StringEquals"
      variable = "${local.eks_oidc_url_no_scheme}:sub"
      values   = ["system:serviceaccount:replay:sa-replay-engine"]
    }
    condition {
      test     = "StringEquals"
      variable = "${local.eks_oidc_url_no_scheme}:aud"
      values   = ["sts.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "sa_replay_engine" {
  name               = "team1-sa-replay-engine"
  assume_role_policy = data.aws_iam_policy_document.irsa_replay_assume.json
  tags               = { Team = "team1", Name = "team1-sa-replay-engine" }
}

data "aws_iam_policy_document" "sa_replay_engine_policy" {
  statement {
    actions   = ["s3:GetObject"]
    resources = ["${aws_s3_bucket.order_records.arn}/*"]
  }
  statement {
    actions   = ["s3:ListBucket"]
    resources = [aws_s3_bucket.order_records.arn]
  }
}

resource "aws_iam_role_policy" "sa_replay_engine" {
  name   = "team1-sa-replay-engine-policy"
  role   = aws_iam_role.sa_replay_engine.id
  policy = data.aws_iam_policy_document.sa_replay_engine_policy.json
}
