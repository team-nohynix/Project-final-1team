# CI에서 도는 Terraform plan/apply용 GitHub Actions 역할 — cicd.tf의 이미지 빌드/배포
# 역할과 의도적으로 분리한다. plan은 매 push마다 사람 개입 없이 자동으로 도는데,
# apply는 실제 인프라를 바꾸는 작업이고 이 계정은 여러 팀이 공유하므로 사람 승인
# 없이는 못 돌게 막아야 한다:
#   - plan 역할: 읽기 전용(ReadOnlyAccess)만 붙인다 — 자동 실행이라도 아무것도
#     바꿀 수 없어야 안전하다.
#   - apply 역할: 실제 쓰기 권한이 필요해서(Terraform이 EC2/EKS/RDS/IAM 등을 직접
#     만들고 바꿈) 광범위하다 — 대신 트러스트 정책을 GitHub Actions
#     "environment" 클레임으로 스코프해서, 오직 그 environment를 쓰는 job만
#     이 역할을 assume할 수 있다. environment에 필수 리뷰어를 설정하는 건
#     GitHub 저장소 Settings에서 사람이 직접 해야 한다(Terraform으로 못 만듦) —
#     이게 실제 "사람 승인 게이트"다, IAM은 그 게이트를 우회 못 하게 보조할 뿐.

locals {
  terraform_apply_environment = "infra-apply"
}

# --- plan (읽기 전용, 자동 실행) ---------------------------------------------

data "aws_iam_policy_document" "github_actions_terraform_plan_assume" {
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
    # 2026-08-18: GitHub sub 클레임 포맷 변경으로 owner/repo 둘 다 불변 숫자 ID가 붙음
    # (cicd.tf의 github_actions_assume 조건 주석 참고) — 이 두 role도 같은 이유로
    # 전체 CI/CD가 실패하고 있었다.
    condition {
      test     = "StringLike"
      variable = "token.actions.githubusercontent.com:sub"
      values = [
        "repo:KimDJ7105/Project-final-1team:ref:refs/heads/prod",
        "repo:KimDJ7105@101383021/Project-final-1team@1314526744:ref:refs/heads/prod",
      ]
    }
  }
}

resource "aws_iam_role" "github_actions_terraform_plan" {
  name               = "team1-github-actions-tf-plan"
  assume_role_policy = data.aws_iam_policy_document.github_actions_terraform_plan_assume.json

  tags = {
    Team = "team1"
    Name = "team1-github-actions-tf-plan"
  }
}

resource "aws_iam_role_policy_attachment" "github_actions_terraform_plan_readonly" {
  role       = aws_iam_role.github_actions_terraform_plan.name
  policy_arn = "arn:aws:iam::aws:policy/ReadOnlyAccess"
}

# ReadOnlyAccess만으로는 S3 네이티브 state locking(각 스택 providers.tf의
# use_lockfile = true)이 안 된다 — plan조차 state를 읽기 전에 .tflock 오브젝트를
# 잠깐 썼다 지워야 해서 s3:PutObject/DeleteObject가 필요하다(2026-08-19, terraform
# plan을 tee로 가리던 pipefail 버그를 고치고 나서야 이게 계속 실패해왔다는 걸
# 발견했다). lock 파일 경로만 좁혀서 허용.
data "aws_iam_policy_document" "github_actions_terraform_plan_lockfile" {
  statement {
    actions = ["s3:PutObject", "s3:DeleteObject"]
    resources = [
      "arn:aws:s3:::team1-terraform-state-s3/network/terraform.tfstate.tflock",
      "arn:aws:s3:::team1-terraform-state-s3/truss/terraform.tfstate.tflock",
    ]
  }
}

resource "aws_iam_role_policy" "github_actions_terraform_plan_lockfile" {
  name   = "team1-github-actions-tf-plan-lockfile-policy"
  role   = aws_iam_role.github_actions_terraform_plan.id
  policy = data.aws_iam_policy_document.github_actions_terraform_plan_lockfile.json
}

# --- apply (쓰기 권한, environment로 게이트) ---------------------------------

data "aws_iam_policy_document" "github_actions_terraform_apply_assume" {
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
    # 2026-08-18: GitHub sub 클레임 포맷 변경으로 owner/repo 둘 다 불변 숫자 ID가 붙음
    # (cicd.tf의 github_actions_assume 조건 주석 참고) — 이 두 role도 같은 이유로
    # 전체 CI/CD가 실패하고 있었다.
    #
    # 2026-08-19: 위 수정 후에도 terraform-apply만 여전히 AssumeRoleWithWebIdentity가
    # AccessDenied — CloudTrail로 실제 요청자를 확인해보니 이 job처럼 `environment:`를
    # 쓰는 job은 sub 클레임이 "repo:OWNER/REPO:ref:refs/heads/BRANCH"가 아니라
    # "repo:OWNER/REPO:environment:ENV_NAME"으로 완전히 다른 형식이 된다(ref 부분이
    # environment 부분으로 대체됨, 둘이 같이 안 붙음). environment 기반 패턴을 추가한다.
    condition {
      test     = "StringLike"
      variable = "token.actions.githubusercontent.com:sub"
      values = [
        "repo:KimDJ7105/Project-final-1team:ref:refs/heads/prod",
        "repo:KimDJ7105@101383021/Project-final-1team@1314526744:ref:refs/heads/prod",
        "repo:KimDJ7105/Project-final-1team:environment:${local.terraform_apply_environment}",
        "repo:KimDJ7105@101383021/Project-final-1team@1314526744:environment:${local.terraform_apply_environment}",
      ]
    }
    # 이 조건이 실제 게이트 — GitHub Environment "infra-apply"에 필수 리뷰어를
    # 설정해두면(저장소 Settings에서 수동 설정), 그 environment를 쓰는 job은
    # 리뷰어가 승인해야만 실행되고, 이 OIDC 클레임도 그때 가서야 발급된다.
    condition {
      test     = "StringEquals"
      variable = "token.actions.githubusercontent.com:environment"
      values   = [local.terraform_apply_environment]
    }
  }
}

resource "aws_iam_role" "github_actions_terraform_apply" {
  name               = "team1-github-actions-tf-apply"
  assume_role_policy = data.aws_iam_policy_document.github_actions_terraform_apply_assume.json

  tags = {
    Team = "team1"
    Name = "team1-github-actions-tf-apply"
  }
}

# 이 스택이 실제로 만드는 서비스만 나열 — AdministratorAccess 대신 서비스 단위로
# 좁혔지만, 각 서비스 안에서는 광범위하다(Terraform이 뭘 더 만들지 미리 다 못
# 정하므로). iam:*은 irsa.tf/cicd.tf 등이 역할·정책을 직접 만들고 바꾸는 것 때문에
# 불가피하다 — 이 계정 공유 특성상 위험한 권한이라는 걸 분명히 인지할 것
# (그래서 environment 승인 게이트가 필요함).
data "aws_iam_policy_document" "github_actions_terraform_apply_policy" {
  statement {
    sid = "TerraformManagedServices"
    actions = [
      "ec2:*",
      "eks:*",
      "rds:*",
      "elasticache:*",
      "kafka:*",
      "s3:*",
      "cloudfront:*",
      "route53:*",
      "wafv2:*",
      "lambda:*",
      "sqs:*",
      "sns:*",
      "logs:*",
      "cloudwatch:*",
      "aps:*",
      "grafana:*",
      "acm:Describe*",
      "acm:List*",
      "acm:Get*",
      "iam:*",
    ]
    resources = ["*"]
  }
}

resource "aws_iam_role_policy" "github_actions_terraform_apply" {
  name   = "team1-github-actions-tf-apply-policy"
  role   = aws_iam_role.github_actions_terraform_apply.id
  policy = data.aws_iam_policy_document.github_actions_terraform_apply_policy.json
}

output "github_actions_terraform_plan_role_arn" {
  value = aws_iam_role.github_actions_terraform_plan.arn
}

output "github_actions_terraform_apply_role_arn" {
  value = aws_iam_role.github_actions_terraform_apply.arn
}

output "terraform_apply_github_environment_name" {
  description = "GitHub 저장소 Settings > Environments에서 이 이름으로 만들고 필수 리뷰어를 설정해야 apply가 실제로 게이트된다"
  value       = local.terraform_apply_environment
}
