# Job 트리거 — SQS + Lambda. 접수API는 Public ALB 뒤에서 외부 노출되므로 Job 생성 RBAC을
# 직접 갖지 않는다: 접수API -> SQS 발행만, Lambda(VPC 연결)가 큐를 소비해 EKS 프라이빗
# 엔드포인트로 Job을 생성한다 — 클러스터 변경 권한 보유 주체를 Lambda 하나로 좁힌다.
#
# Lambda 함수 코드는 lambda/job-trigger/index.py의 자리표시자이며, 실제 로직은 CI/CD가 배포한다.

resource "aws_sqs_queue" "job_trigger_dlq" {
  name                      = "team1-sqs-job-trigger-dlq"
  message_retention_seconds = 1209600 # 14일 — 실패 원인 조사 시간 확보

  tags = {
    Team = "team1"
    Name = "team1-sqs-job-trigger-dlq"
  }
}

resource "aws_sqs_queue" "job_trigger" {
  name                       = "team1-sqs-job-trigger"
  visibility_timeout_seconds = 60 # Lambda 타임아웃(30초)의 6배 이상 권장치

  redrive_policy = jsonencode({
    deadLetterTargetArn = aws_sqs_queue.job_trigger_dlq.arn
    maxReceiveCount     = 3
  })

  tags = {
    Team = "team1"
    Name = "team1-sqs-job-trigger"
  }
}

data "archive_file" "job_trigger_lambda" {
  type        = "zip"
  source_file = "${path.module}/lambda/job-trigger/index.py"
  output_path = "${path.module}/lambda/job-trigger.zip"
}

data "aws_iam_policy_document" "lambda_job_trigger_assume" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["lambda.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "lambda_job_trigger" {
  name               = "team1-lambda-job-trigger-role"
  assume_role_policy = data.aws_iam_policy_document.lambda_job_trigger_assume.json

  tags = {
    Team = "team1"
    Name = "team1-lambda-job-trigger-role"
  }
}

# VPC 연결(ENI 생성/삭제)용 — 로그 권한은 포함돼 있지 않아 Basic과 둘 다 필요하다.
resource "aws_iam_role_policy_attachment" "lambda_job_trigger_vpc" {
  role       = aws_iam_role.lambda_job_trigger.name
  policy_arn = "arn:aws:iam::aws:policy/service-role/AWSLambdaVPCAccessExecutionRole"
}

# CloudWatch Logs 쓰기 권한(CreateLogGroup/CreateLogStream/PutLogEvents) — 위 VPC 정책에는
# 없어서 빠뜨리면 Lambda가 로그를 아예 못 남긴다.
resource "aws_iam_role_policy_attachment" "lambda_job_trigger_basic" {
  role       = aws_iam_role.lambda_job_trigger.name
  policy_arn = "arn:aws:iam::aws:policy/service-role/AWSLambdaBasicExecutionRole"
}

data "aws_iam_policy_document" "lambda_job_trigger_policy" {
  statement {
    actions = [
      "sqs:ReceiveMessage",
      "sqs:DeleteMessage",
      "sqs:GetQueueAttributes",
    ]
    resources = [aws_sqs_queue.job_trigger.arn]
  }
  statement {
    actions   = ["eks:DescribeCluster"]
    resources = [aws_eks_cluster.team1.arn]
  }
}

resource "aws_iam_role_policy" "lambda_job_trigger" {
  name   = "team1-lambda-job-trigger-policy"
  role   = aws_iam_role.lambda_job_trigger.id
  policy = data.aws_iam_policy_document.lambda_job_trigger_policy.json
}

resource "aws_lambda_function" "job_trigger" {
  function_name = "team1-lambda-job-trigger"
  role          = aws_iam_role.lambda_job_trigger.arn
  handler       = "index.handler"
  runtime       = "python3.13"
  timeout       = 30
  memory_size   = 256

  filename         = data.archive_file.job_trigger_lambda.output_path
  source_code_hash = data.archive_file.job_trigger_lambda.output_base64sha256

  vpc_config {
    subnet_ids = [
      data.terraform_remote_state.network.outputs.subnet_ids.eks_backend.a,
      data.terraform_remote_state.network.outputs.subnet_ids.eks_backend.b,
    ]
    security_group_ids = [data.terraform_remote_state.network.outputs.security_group_ids.lambda_job_trigger]
  }

  environment {
    variables = {
      EKS_CLUSTER_NAME = aws_eks_cluster.team1.name
    }
  }

  tags = {
    Team = "team1"
    Name = "team1-lambda-job-trigger"
  }

  depends_on = [
    aws_iam_role_policy_attachment.lambda_job_trigger_vpc,
    aws_iam_role_policy_attachment.lambda_job_trigger_basic,
  ]
}

resource "aws_lambda_event_source_mapping" "job_trigger" {
  event_source_arn = aws_sqs_queue.job_trigger.arn
  function_name    = aws_lambda_function.job_trigger.arn
  batch_size       = 1
}

# AWS 관리형 액세스 정책은 붙이지 않고 kubernetes_groups로 매핑만 해둔다 — 실제 RBAC
# Role(ai-trader/replay 네임스페이스, jobs에 create/get/list/watch/delete)과 RoleBinding은
# k8s/job-trigger-rbac.yaml에서 적용한다.
resource "aws_eks_access_entry" "lambda_job_trigger" {
  cluster_name      = aws_eks_cluster.team1.name
  principal_arn     = aws_iam_role.lambda_job_trigger.arn
  type              = "STANDARD"
  kubernetes_groups = ["team1-job-trigger-lambda"]
}
