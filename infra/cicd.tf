# GitHub Actions가 team1-truss ECR에 이미지를 push할 수 있게 하는 IAM — 액세스 키 대신
# GitHub의 OIDC 토큰으로 역할을 assume한다. main 브랜치에서 실행되는 워크플로만 허용한다
# (PR/다른 브랜치까지 열어주려면 트러스트 정책의 sub 조건을 넓히면 된다).
#
# OIDC 프로바이더는 계정에 URL당 1개만 존재 가능 — 공유 계정이라 다른 팀이 이미 만들어둔
# 것을 그대로 참조한다(직접 만들려다 EntityAlreadyExists로 확인).
data "aws_iam_openid_connect_provider" "github_actions" {
  url = "https://token.actions.githubusercontent.com"
}

data "aws_iam_policy_document" "github_actions_assume" {
  statement {
    actions = ["sts:AssumeRoleWithWebIdentity"]
    principals {
      type        = "Federated"
      identifiers = [data.aws_iam_openid_connect_provider.github_actions.arn]
    }
    condition {
      test     = "StringEquals"
      variable = "token.actions.githubusercontent.com:aud"
      values   = ["sts.amazonaws.com"]
    }
    condition {
      test     = "StringLike"
      variable = "token.actions.githubusercontent.com:sub"
      values   = ["repo:KimDJ7105/Project-final-1team:ref:refs/heads/main"]
    }
  }
}

resource "aws_iam_role" "github_actions_ecr_push" {
  name               = "team1-github-actions-ecr-push"
  assume_role_policy = data.aws_iam_policy_document.github_actions_assume.json

  tags = {
    Team = "team1"
    Name = "team1-github-actions-ecr-push"
  }
}

data "aws_iam_policy_document" "github_actions_ecr_push_policy" {
  statement {
    actions   = ["ecr:GetAuthorizationToken"]
    resources = ["*"]
  }
  statement {
    actions = [
      "ecr:BatchCheckLayerAvailability",
      "ecr:GetDownloadUrlForLayer",
      "ecr:BatchGetImage",
      "ecr:InitiateLayerUpload",
      "ecr:UploadLayerPart",
      "ecr:CompleteLayerUpload",
      "ecr:PutImage",
    ]
    resources = [aws_ecr_repository.team1.arn]
  }
}

resource "aws_iam_role_policy" "github_actions_ecr_push" {
  name   = "team1-github-actions-ecr-push-policy"
  role   = aws_iam_role.github_actions_ecr_push.id
  policy = data.aws_iam_policy_document.github_actions_ecr_push_policy.json
}

# Job 트리거 Lambda 코드 갱신 권한(lambda/job-trigger/index.py는 자리표시자 — 실제 배포는
# CI/CD가 이 역할로 담당).
data "aws_iam_policy_document" "github_actions_lambda_deploy_policy" {
  statement {
    actions   = ["lambda:UpdateFunctionCode", "lambda:GetFunction"]
    resources = [aws_lambda_function.job_trigger.arn]
  }
}

resource "aws_iam_role_policy" "github_actions_lambda_deploy" {
  name   = "team1-github-actions-lambda-deploy-policy"
  role   = aws_iam_role.github_actions_ecr_push.id
  policy = data.aws_iam_policy_document.github_actions_lambda_deploy_policy.json
}

output "github_actions_role_arn" {
  description = "GitHub Actions 워크플로의 aws-actions/configure-aws-credentials에 넣을 role-to-assume"
  value       = aws_iam_role.github_actions_ecr_push.arn
}
