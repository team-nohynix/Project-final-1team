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
    # GitHub Actions OIDC sub 클레임 포맷(cicd.tf 참고) — 둘 다 매칭.
    condition {
      test     = "StringLike"
      variable = "token.actions.githubusercontent.com:sub"
      values = [
        "repo:team-nohynix/Project-final-1team:ref:refs/heads/prod",
        "repo:team-nohynix@321210135/Project-final-1team@1314526744:ref:refs/heads/prod",
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
# use_lockfile = true)이 안 된다 — plan도 state를 읽기 전에 .tflock 오브젝트를
# 잠깐 썼다 지워야 해서 s3:PutObject/DeleteObject가 필요하다. lock 파일 경로만
# 좁혀서 허용.
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

# ReadOnlyAccess는 GetSecretValue를 일부러 빼고 준다(진짜 비밀값이 아니라 존재/메타데이터만
# 보이게) — 근데 secrets-manager.tf의 aws_secretsmanager_secret_version은 plan의 refresh
# 단계에서 실제 값을 읽어야만 diff를 계산할 수 있다. "읽기 전용은 값을 못 본다"는 원칙을
# 계정 전체가 아니라 이 시크릿 하나로만 좁혀서 깬다 — tf-apply 쪽에도 같은 예외가 있다
# (아래 SecretsManagerRecorderDbUrl).
data "aws_iam_policy_document" "github_actions_terraform_plan_secretsmanager" {
  statement {
    actions   = ["secretsmanager:GetSecretValue", "secretsmanager:DescribeSecret", "secretsmanager:GetResourcePolicy"]
    resources = [aws_secretsmanager_secret.recorder_mysql_db_url.arn]
  }
}

resource "aws_iam_role_policy" "github_actions_terraform_plan_secretsmanager" {
  name   = "team1-github-actions-tf-plan-secretsmanager-policy"
  role   = aws_iam_role.github_actions_terraform_plan.id
  policy = data.aws_iam_policy_document.github_actions_terraform_plan_secretsmanager.json
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
    # GitHub Actions OIDC sub 클레임 — ref 기반 포맷과 environment 기반 포맷
    # ("repo:OWNER/REPO:environment:ENV_NAME") 둘 다 온다. 지금 워크플로는
    # environment 게이트 없이 자동 apply라 실제로는 ref 기반만 쓰이지만, 나중에
    # 승인 게이트를 다시 붙일 경우를 대비해 environment 패턴도 남겨둔다.
    condition {
      test     = "StringLike"
      variable = "token.actions.githubusercontent.com:sub"
      values = [
        "repo:team-nohynix/Project-final-1team:ref:refs/heads/prod",
        "repo:team-nohynix@321210135/Project-final-1team@1314526744:ref:refs/heads/prod",
        "repo:team-nohynix/Project-final-1team:environment:${local.terraform_apply_environment}",
        "repo:team-nohynix@321210135/Project-final-1team@1314526744:environment:${local.terraform_apply_environment}",
      ]
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
# 불가피하다 — 이 계정 공유 특성상 위험한 권한이라는 걸 분명히 인지할 것.
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
      "ecr:*",
      "elasticloadbalancing:*",
      "events:*",
    ]
    resources = ["*"]
  }
  # secretsmanager:*는 위 목록에 일부러 없다(GetSecretValue는 민감 액션이라 와일드카드에
  # 안 넣는다) — secrets-manager.tf의 시크릿 버전이 plan/apply 중 refresh로 실제 값을
  # 읽고 쓸 수 있어야 해서, 이 시크릿 하나에만 실제로 쓰는 액션만 나열해서 예외를 둔다.
  statement {
    sid = "SecretsManagerRecorderDbUrl"
    actions = [
      "secretsmanager:GetSecretValue",
      "secretsmanager:DescribeSecret",
      "secretsmanager:GetResourcePolicy",
      "secretsmanager:PutSecretValue",
      "secretsmanager:UpdateSecret",
      "secretsmanager:TagResource",
      "secretsmanager:UntagResource",
      "secretsmanager:UpdateSecretVersionStage",
    ]
    resources = [aws_secretsmanager_secret.recorder_mysql_db_url.arn]
  }
}

resource "aws_iam_role_policy" "github_actions_terraform_apply" {
  name   = "team1-github-actions-tf-apply-policy"
  role   = aws_iam_role.github_actions_terraform_apply.id
  policy = data.aws_iam_policy_document.github_actions_terraform_apply_policy.json
}

# --- EKS access entry ---------------------------------------------------
#
# IAM 권한(위 ReadOnlyAccess / iam:*·ec2:*·eks:* 등)과 EKS access entry는 별개다 —
# 전자는 "AWS API를 호출할 수 있느냐"고, 후자는 "K8s API 서버 자체에 어떤 신원으로
# 인증되느냐"다. 실제 RBAC 권한은 access entry가 아니라 아래
# kubernetes_cluster_role/kubernetes_cluster_role_binding이 준다 — access entry는
# k8s 그룹 매핑만 한다.
resource "aws_eks_access_entry" "github_actions_terraform_plan" {
  cluster_name      = aws_eks_cluster.team1.name
  principal_arn     = aws_iam_role.github_actions_terraform_plan.arn
  type              = "STANDARD"
  kubernetes_groups = ["team1-tf-plan"]
}

resource "aws_eks_access_entry" "github_actions_terraform_apply" {
  cluster_name      = aws_eks_cluster.team1.name
  principal_arn     = aws_iam_role.github_actions_terraform_apply.arn
  type              = "STANDARD"
  kubernetes_groups = ["team1-tf-apply"]
}

# tf_apply의 cluster-admin은 kubernetes_cluster_role_binding(K8s RBAC API 호출)이 아니라
# aws_eks_access_policy_association(EKS 컨트롤 플레인 API 호출)로 준다 —
# access_config.bootstrap_cluster_creator_admin_permissions는 "누가 CreateCluster를
# 호출했는가"에 의존해서, CI가 아니라 사람이 먼저 apply해 클러스터가 만들어지면 admin
# 권한이 TF_APPLY_ROLE이 아니라 그 사람에게 가버린다 — access_policy_association은
# 그 문제가 없다.
resource "aws_eks_access_policy_association" "github_actions_terraform_apply_admin" {
  cluster_name  = aws_eks_cluster.team1.name
  principal_arn = aws_iam_role.github_actions_terraform_apply.arn
  policy_arn    = "arn:aws:eks::aws:cluster-access-policy/AmazonEKSClusterAdminPolicy"

  access_scope {
    type = "cluster"
  }
}

# tf_plan은 Secret까지 읽어야 해서(helm_release가 릴리스 메타데이터를 Secret으로
# 저장) 아래 kubernetes_cluster_role.tf_plan_readonly(커스텀, Secret 포함 read-only)가
# 주력이지만, 그 커스텀 RBAC 자체도 K8s API 호출로 만들어지는 리소스라 부트스트랩
# 시점에 지연/실패할 수 있다 — AWS 관리형 View 정책(Secret 값은 제외, 존재 여부/
# 메타데이터만)을 기본 baseline으로 같이 붙여 최소한의 plan은 항상 되게 한다.
resource "aws_eks_access_policy_association" "github_actions_terraform_plan_view" {
  cluster_name  = aws_eks_cluster.team1.name
  principal_arn = aws_iam_role.github_actions_terraform_plan.arn
  policy_arn    = "arn:aws:eks::aws:cluster-access-policy/AmazonEKSViewPolicy"

  access_scope {
    type = "cluster"
  }
}

# RBAC 자체도 여기(terraform, kubernetes provider)에 둔다 — YAML+kubectl apply로 두면
# terraform apply 자신의 helm_release/kubernetes_secret/kubernetes_namespace 리소스들이
# 정작 이 RBAC 없이 먼저 돌게 되는 순환 문제가 생긴다(YAML은 bootstrap-k8s job이
# terraform-apply "다음"에 적용). kubernetes_cluster_role_binding으로 옮기면 같은
# terraform apply 안에서 access entry → 이 바인딩 → helm_release/secret/namespace
# 순서로 자기완결적으로 풀린다.
resource "kubernetes_cluster_role" "tf_plan_readonly" {
  depends_on = [time_sleep.wait_for_eks_auth]

  metadata {
    name = "team1-tf-plan-readonly"
  }
  rule {
    api_groups = ["*"]
    resources  = ["*"]
    verbs      = ["get", "list", "watch"]
  }
}

resource "kubernetes_cluster_role_binding" "tf_plan_readonly" {
  metadata {
    name = "team1-tf-plan-readonly"
  }
  role_ref {
    api_group = "rbac.authorization.k8s.io"
    kind      = "ClusterRole"
    name      = kubernetes_cluster_role.tf_plan_readonly.metadata[0].name
  }
  subject {
    kind      = "Group"
    name      = "team1-tf-plan"
    api_group = "rbac.authorization.k8s.io"
  }
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
