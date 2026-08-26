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
    # prod 브랜치 push로 전체 CI/CD(프론트 S3, 이미지 ECR, k8s 롤아웃 재시작)를 돌린다 —
    # main은 예전 backend 전용 워크플로 흔적, 새 워크플로는 prod만 쓰지만 트러스트는
    # 굳이 좁히지 않고 남겨둔다(레포에 남아있는 워크플로 파일 자체가 실제 방아쇠라
    # 여기 조건 하나만으로 뭐가 도는지 결정되지 않음).
    #
    # 2026-08-18: GitHub가 sub 클레임 포맷을 바꿔서 owner/repo 둘 다 뒤에 불변 숫자 ID가
    # 붙는다(repo:OWNER@OWNER_ID/REPO@REPO_ID:ref:...) — repo ID(@1314526744)만 추가하고
    # owner ID(@101383021)는 빠뜨려서 전체 CI/CD가 계속 실패하고 있었다(실제 OIDC 토큰의
    # sub 클레임을 디버그 job으로 직접 확인해서 찾음). 예전 포맷 두 개는 이제 안 쓰이는
    # 걸로 보이지만, 혹시 GitHub가 롤백하는 경우를 대비해 남겨둔다.
    condition {
      test     = "StringLike"
      variable = "token.actions.githubusercontent.com:sub"
      values = [
        "repo:team-nohynix/Project-final-1team:ref:refs/heads/main",
        "repo:team-nohynix/Project-final-1team:ref:refs/heads/prod",
        "repo:team-nohynix@321210135/Project-final-1team@1314526744:ref:refs/heads/main",
        "repo:team-nohynix@321210135/Project-final-1team@1314526744:ref:refs/heads/prod",
      ]
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

# 프론트 배포(S3 동기화 + CloudFront 무효화) — infra/edge.tf의 team1-truss-frontend
# 버킷과 그 CloudFront 배포에만 스코프.
data "aws_iam_policy_document" "github_actions_frontend_deploy_policy" {
  statement {
    actions   = ["s3:PutObject", "s3:DeleteObject", "s3:ListBucket"]
    resources = [aws_s3_bucket.frontend.arn, "${aws_s3_bucket.frontend.arn}/*"]
  }
  statement {
    actions   = ["cloudfront:CreateInvalidation"]
    resources = [aws_cloudfront_distribution.frontend.arn]
  }
  # ListDistributions는 IAM에서 리소스 단위로 못 좁힌다(항상 "*") — 배포 스크립트가
  # frontend distribution ID를 하드코딩하지 않고 도메인으로 찾으려면 필요하다.
  # 읽기 전용이라 공유 계정의 다른 팀 리소스를 조회는 하지만 바꾸진 못한다.
  statement {
    actions   = ["cloudfront:ListDistributions"]
    resources = ["*"]
  }
}

resource "aws_iam_role_policy" "github_actions_frontend_deploy" {
  name   = "team1-github-actions-frontend-deploy-policy"
  role   = aws_iam_role.github_actions_ecr_push.id
  policy = data.aws_iam_policy_document.github_actions_frontend_deploy_policy.json
}

# kubectl rollout restart용 — EKS API 접근 자체는 DescribeCluster(엔드포인트/CA 조회)만
# IAM으로 필요하고, 실제 k8s 권한은 access entry+RBAC(아래 aws_eks_access_entry.github_actions,
# k8s/ci-deploy-rbac.yaml)가 준다.
data "aws_iam_policy_document" "github_actions_eks_describe_policy" {
  statement {
    actions   = ["eks:DescribeCluster"]
    resources = [aws_eks_cluster.team1.arn]
  }
}

resource "aws_iam_role_policy" "github_actions_eks_describe" {
  name   = "team1-github-actions-eks-describe-policy"
  role   = aws_iam_role.github_actions_ecr_push.id
  policy = data.aws_iam_policy_document.github_actions_eks_describe_policy.json
}

# CI가 만든 새 이미지(backend/orderapi/matching/recorder — Deployment로 상시 실행되는
# 4개만. trader/replayengine은 Job으로 그때그때 뜨므로 :latest를 다음 실행 때 자연히
# 받아서 재시작이 필요 없다)를 즉시 반영하려면 rollout restart 권한이 필요하다 —
# job-trigger Lambda와 같은 패턴(access entry로 k8s 그룹 매핑, 실제 권한은 RBAC).
resource "aws_eks_access_entry" "github_actions" {
  cluster_name      = aws_eks_cluster.team1.name
  principal_arn     = aws_iam_role.github_actions_ecr_push.arn
  type              = "STANDARD"
  kubernetes_groups = ["team1-github-actions-deploy"]
}

output "github_actions_role_arn" {
  description = "GitHub Actions 워크플로의 aws-actions/configure-aws-credentials에 넣을 role-to-assume"
  value       = aws_iam_role.github_actions_ecr_push.arn
}
