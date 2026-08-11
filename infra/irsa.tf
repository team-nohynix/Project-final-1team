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
    actions   = ["kafka-cluster:Connect"]
    resources = [aws_msk_serverless_cluster.team1_truss.arn]
  }
  statement {
    actions   = ["kafka-cluster:WriteData", "kafka-cluster:DescribeTopic"]
    resources = ["${local.msk_topic_arn_prefix}/orders"]
  }
  statement {
    actions   = ["sqs:SendMessage"]
    resources = [aws_sqs_queue.job_trigger.arn]
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
    actions   = ["kafka-cluster:Connect"]
    resources = [aws_msk_serverless_cluster.team1_truss.arn]
  }
  statement {
    actions = ["kafka-cluster:ReadData", "kafka-cluster:WriteData", "kafka-cluster:DescribeTopic"]
    resources = [
      "${local.msk_topic_arn_prefix}/orders",
      "${local.msk_topic_arn_prefix}/executions",
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
    actions   = ["kafka-cluster:ReadData", "kafka-cluster:DescribeTopic"]
    resources = ["${local.msk_topic_arn_prefix}/executions"]
  }
  statement {
    actions   = ["kafka-cluster:AlterGroup", "kafka-cluster:DescribeGroup"]
    resources = ["${local.msk_group_arn_prefix}/*"]
  }
  statement {
    actions   = ["secretsmanager:GetSecretValue"]
    resources = [aws_rds_cluster.team1_truss.master_user_secret[0].secret_arn]
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
    actions   = ["s3:PutObject"]
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
    actions   = ["s3:PutObject"]
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
  description = "Bedrock 모델을 실제로 호출하는 리전 — ap-northeast-2 가용 여부 확인 전까지는 us-east-1 기본값"
  type        = string
  default     = "us-east-1"
}

data "aws_iam_policy_document" "sa_ai_trader_bedrock_policy" {
  statement {
    actions = ["bedrock:InvokeModel"]
    resources = [
      "arn:aws:bedrock:${var.bedrock_model_region}::foundation-model/anthropic.claude-sonnet-5*",
      "arn:aws:bedrock:${var.bedrock_model_region}::foundation-model/anthropic.claude-haiku-4-5*",
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
