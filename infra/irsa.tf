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
    # WriteDataIdempotently — Kafka 3.0+ 클라이언트가 기본으로 켜는 producer idempotence는
    # MSK IAM에서 토픽 단위 WriteData와 별개로 클러스터 단위 이 권한을 요구한다.
    actions   = ["kafka-cluster:Connect", "kafka-cluster:WriteDataIdempotently"]
    resources = [aws_msk_serverless_cluster.team1_truss.arn]
  }
  statement {
    actions   = ["kafka-cluster:WriteData", "kafka-cluster:DescribeTopic"]
    resources = ["${local.msk_topic_arn_prefix}/orders"]
  }
  statement {
    # executions — 체결 결과를 접수 API가 되읽어 응답에 반영하므로 컨슈머 그룹도 붙는다.
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
  # 읽는다 — sa-replay-engine과 같은 읽기 전용 권한.
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

# Redis AUTH 토큰(elasticache.tf) — CSI Secrets Store가 orderapi 파드 자신의 IRSA를
# 그대로 쓴다(redis-auth-secret-provider.yaml).
data "aws_iam_policy_document" "sa_ingest_api_secretsmanager" {
  statement {
    actions   = ["secretsmanager:GetSecretValue", "secretsmanager:DescribeSecret"]
    resources = [aws_secretsmanager_secret.redis_auth_token.arn]
  }
}

resource "aws_iam_role_policy" "sa_ingest_api_secretsmanager" {
  name   = "team1-ingest-api-secretsmanager-read"
  role   = aws_iam_role.sa_ingest_api.id
  policy = data.aws_iam_policy_document.sa_ingest_api_secretsmanager.json
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
    # WriteDataIdempotently — sa_ingest_api와 동일한 이유(Kafka 3.0+ 기본 idempotent producer).
    actions   = ["kafka-cluster:Connect", "kafka-cluster:WriteDataIdempotently"]
    resources = [aws_msk_serverless_cluster.team1_truss.arn]
  }
  statement {
    actions = ["kafka-cluster:ReadData", "kafka-cluster:WriteData", "kafka-cluster:DescribeTopic"]
    resources = [
      "${local.msk_topic_arn_prefix}/orders",
      "${local.msk_topic_arn_prefix}/executions",
      # assignments — 마켓 재분배 조율용.
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

# Redis AUTH 토큰(elasticache.tf) — sa_ingest_api_secretsmanager와 같은 이유.
data "aws_iam_policy_document" "sa_matching_engine_secretsmanager" {
  statement {
    actions   = ["secretsmanager:GetSecretValue", "secretsmanager:DescribeSecret"]
    resources = [aws_secretsmanager_secret.redis_auth_token.arn]
  }
}

resource "aws_iam_role_policy" "sa_matching_engine_secretsmanager" {
  name   = "team1-matching-engine-secretsmanager-read"
  role   = aws_iam_role.sa_matching_engine.id
  policy = data.aws_iam_policy_document.sa_matching_engine_secretsmanager.json
}

# ---------------------------------------------------------------------------
# sa-recorder (backend) — MSK 구독(orders/executions/assignments), S3 PutObject
# (trade-results), Secrets Manager(DB URL·Redis 토큰) 읽기.

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

# Secrets Store CSI Driver(csi-secrets-store.tf)가 마운트하는 파드 자신의 IRSA를 그대로
# 써서 Secrets Manager를 읽으므로(드라이버 자체엔 별도 IRSA가 없음), sa-recorder 역할에
# 인라인 정책으로 붙인다. DB URL(recorder-db-secret-provider.yaml)과 Redis AUTH 토큰
# (redis-auth-secret-provider.yaml) 둘 다 recorder가 필요로 한다.
data "aws_iam_policy_document" "sa_recorder_secretsmanager" {
  statement {
    actions = ["secretsmanager:GetSecretValue", "secretsmanager:DescribeSecret"]
    resources = [
      aws_secretsmanager_secret.recorder_mysql_db_url.arn,
      aws_secretsmanager_secret.redis_auth_token.arn,
    ]
  }
}

resource "aws_iam_role_policy" "sa_recorder_secretsmanager" {
  name   = "team1-recorder-secretsmanager-read"
  role   = aws_iam_role.sa_recorder.id
  policy = data.aws_iam_policy_document.sa_recorder_secretsmanager.json
}

# ---------------------------------------------------------------------------
# sa-collector (시세 수집기, collector ns) — S3 GetObject/PutObject(market-data). Kafka 미사용.

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
    # GetObject — backend/server.go의 fileHandler가 캐시 히트 시 직접 읽어서 서빙한다
    # (HeadObject로 존재 확인 후 미스일 때만 새로 수집) — 쓰기 전용이 아니다.
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
    # trader가 주문 기록을 저장할 때(orderstore/s3.go) 기존 파일을 GetObject로 먼저
    # 읽어 병합한 뒤 PutObject로 다시 쓴다 — 쓰기 전용이 아니다.
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
  description = "Bedrock 모델을 실제로 호출하는 리전"
  type        = string
  default     = "ap-northeast-2"
}

# 이 계정에서 쓸 수 있는 최신 Claude 모델은 raw 모델 ID 온디맨드 직접 호출이 막혀 있고
# 추론 프로파일을 통해서만 호출 가능하다. apac.anthropic.claude-3-haiku-20240307-v1:0
# (APAC 추론 프로파일, "가장 저렴한 모델" 팀 결정에 부합)을 쓴다. 추론 프로파일은 등록된
# 여러 리전의 파운데이션 모델로 라우팅되므로, 프로파일 ARN뿐 아니라 그 리전들의
# foundation-model ARN에도 권한이 있어야 한다.
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
