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

# ReadOnlyAccess는 GetSecretValue를 일부러 빼고 준다(진짜 비밀값이 아니라 존재/메타데이터만
# 보이게) — 근데 secrets-manager.tf의 aws_secretsmanager_secret_version은 plan의 refresh
# 단계에서 실제 값을 읽어야만 diff를 계산할 수 있다. 2026-08-27, CI에서
# "AccessDeniedException: ... secretsmanager:GetSecretValue"로 실측(prod push 직후
# terraform-plan 실패, apply는 안 돎). "읽기 전용은 값을 못 본다"는 원칙을 계정
# 전체가 아니라 이 시크릿 하나로만 좁혀서 깬다 — tf-apply 쪽에도 같은 예외가 있다
# (cicd-terraform.tf의 SecretsManagerRecorderDbUrl 참고).
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
    # 2026-08-18: GitHub sub 클레임 포맷 변경으로 owner/repo 둘 다 불변 숫자 ID가 붙음
    # (cicd.tf의 github_actions_assume 조건 주석 참고) — 이 두 role도 같은 이유로
    # 전체 CI/CD가 실패하고 있었다.
    #
    # 2026-08-19: 위 수정 후에도 terraform-apply만 여전히 AssumeRoleWithWebIdentity가
    # AccessDenied — CloudTrail로 실제 요청자를 확인해보니 이 job처럼 `environment:`를
    # 쓰는 job은 sub 클레임이 "repo:OWNER/REPO:ref:refs/heads/BRANCH"가 아니라
    # "repo:OWNER/REPO:environment:ENV_NAME"으로 완전히 다른 형식이 된다(ref 부분이
    # environment 부분으로 대체됨, 둘이 같이 안 붙음). environment 기반 패턴도 추가해뒀다.
    #
    # 2026-08-19(같은 날 두 번째): 워크플로에서 environment: infra-apply 자체를
    # 뗐다(사용자 요청으로 사람 승인 게이트 없이 자동 apply하도록 변경 — 이 계정이
    # 여러 팀 공유이고 이 role의 권한이 광범위해서 리스크가 있다는 걸 알고 진행한
    # 선택). environment 클레임이 더 이상 안 붙으니 sub는 항상 ref 기반으로 돌아오고,
    # 아래 environment StringEquals 조건은 제거했다 — environment 기반 sub 패턴 2개는
    # 당장 안 쓰이지만, 나중에 승인 게이트를 다시 붙일 경우를 대비해 남겨둔다.
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
#
# 2026-08-19: environment 승인 게이트를 뗀 뒤 처음 실제로 끝까지 apply를 돌려보고서야
# ecr:*/elasticloadbalancing:*/events:*가 원래부터 빠져 있었다는 걸 발견했다 —
# 지금까지는 사람이 승인해야만 apply가 실행됐는데, 실제로는 root stack이 한 번도
# 끝까지 성공한 적이 없었던 것으로 보인다(ecr.tf의 ECR repo, edge.tf의 data "aws_lb",
# karpenter.tf의 EventBridge rule을 이 role이 원래도 못 읽었다).
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
  # 2026-08-27: 위 목록엔 secretsmanager:*가 없다(GetSecretValue는 위 "*"에도
  # 일부러 안 넣어온 민감 액션 부류) — secrets-manager.tf의 aws_secretsmanager_secret_version이
  # plan/apply 중 refresh로 실제 값을 읽어야 해서, 그 한 시크릿에만 좁혀서 예외를 둔다.
  # apply는 읽기뿐 아니라 실제로 값/설명/태그를 쓸 수도 있어야 한다 — CI에서
  # GetSecretValue, 그다음 GetResourcePolicy 순서로 하나씩 막힌 걸 실측하고 나서,
  # 와일드카드(secretsmanager:*) 대신 실제로 쓰는 액션만 하나씩 나열했다(계정
  # 전체가 아니라 이 시크릿 하나로 리소스를 좁혔어도, 위 TerraformManagedServices의
  # "서비스 단위로 좁히기" 원칙에 맞춰 액션도 최대한 좁힌다).
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

# --- EKS access entry (2026-08-24 추가) --------------------------------------
#
# IAM 권한(위 ReadOnlyAccess / iam:*·ec2:*·eks:* 등)과 EKS access entry는 별개다 —
# 전자는 "AWS API를 호출할 수 있느냐"고, 후자는 "K8s API 서버 자체에 어떤 신원으로
# 인증되느냐"다. helm_release/kubernetes_secret 리소스(karpenter.tf/alb-controller.tf/
# k8s-addons.tf/mysql-ec2.tf)를 추가하기 전까지는 이 두 role이 K8s 리소스를 전혀 안
# 건드려서 이 access entry가 없어도 아무 문제가 없었는데, 추가하자마자 terraform plan이
# "Kubernetes cluster unreachable: the server has asked for the client to provide
# credentials"로 바로 실패해서 발견했다. 실제 RBAC 권한 자체는 access entry가 아니라
# infra/k8s/terraform-eks-rbac.yaml(ClusterRole/ClusterRoleBinding)이 준다 —
# github_actions_eks_describe 위 주석과 같은 "access entry로 그룹 매핑, 권한은 RBAC"
# 패턴. plan은 읽기 전용(Secret 포함 — Helm 3가 릴리스 메타데이터를 Secret으로
# 저장해서 helm_release를 plan하는 데도 필요), apply는 cluster-admin(이 role이 이미
# 가진 IAM 권한 수준의 자연스러운 연장 — terraform-apply job 주석 참고, 사람 승인
# 게이트가 없다는 전제는 그대로).
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

# tf_apply의 cluster-admin은 원래 kubernetes_cluster_role_binding(K8s RBAC API 호출)로만
# 줬었는데, 이건 access_config.bootstrap_cluster_creator_admin_permissions("클러스터를
# 만든 바로 그 principal에게 자동으로 admin을 준다")에 실질적으로 기대는 구조라 두 가지로
# 깨진다: (1) 이 자동 admin은 EKS 클러스터가 ACTIVE된 시점부터 K8s API 서버 인증
# 레이어에 실제로 반영되기까지 지연이 있을 수 있고(2026-08-25, destroy→apply 리허설
# 중 실측 — Karpenter/Lambda/모니터링 EC2가 겪은 것과 같은 종류의 AWS eventual-
# consistency), (2) 더 근본적으로 "누가 CreateCluster를 호출했는가"에 전적으로
# 의존한다 — CI가 아니라 사람이 로컬에서 terraform apply -target=...으로 (의도치
# 않게 EKS가 딸려 들어와서) 클러스터를 먼저 만들어버리면, 그 admin 권한은
# TF_APPLY_ROLE이 아니라 그 사람의 IAM 정체성으로 가버려서 이후 CI가 영원히
# 403을 받는다(2026-08-25 실측 — CloudTrail로 원인 확인).
# aws_eks_access_policy_association은 K8s API가 아니라 EKS 컨트롤 플레인 API
# 호출이라 이 두 문제 다 없다 — "누가 클러스터를 만들었나"와 무관하게, TF_APPLY_ROLE이
# access entry를 만들 수 있는 IAM 권한만 있으면 즉시 유효해진다.
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
# 여전히 주력이지만, 그 커스텀 RBAC 자체도 K8s API 호출로 만들어지는 리소스라 위와
# 같은 이유로 부트스트랩 시점에 지연/실패할 수 있다 — AWS 관리형 View 정책(Secret
# 값은 제외, 존재 여부/메타데이터만)을 기본 baseline으로 같이 붙여 최소한의 plan은
# 항상 되게 한다.
resource "aws_eks_access_policy_association" "github_actions_terraform_plan_view" {
  cluster_name  = aws_eks_cluster.team1.name
  principal_arn = aws_iam_role.github_actions_terraform_plan.arn
  policy_arn    = "arn:aws:eks::aws:cluster-access-policy/AmazonEKSViewPolicy"

  access_scope {
    type = "cluster"
  }
}

# RBAC 자체도 여기(terraform, kubernetes provider)에 둔다 — 처음엔 infra/k8s/의
# 다른 RBAC들처럼 YAML+kubectl apply로 뒀었는데, 그러면 순환 문제가 생긴다:
# terraform apply 자체가(같은 apply 안의 helm_release/kubernetes_secret/
# kubernetes_namespace 리소스들이) 이 RBAC이 이미 존재해야 K8s API에 붙을 수
# 있는데, YAML은 bootstrap-k8s job이 terraform-apply "다음"에 적용하니 정작
# terraform-apply 자기 자신이 그 RBAC 없이 먼저 돌게 된다 — EKS를 정말로
# 처음부터(access entry도, 이 RBAC도 전혀 없는 상태에서) 복구해야 하는
# 시나리오에서 막힌다. kubernetes_cluster_role_binding으로 옮기면 같은 terraform
# apply 안에서 access entry → 이 바인딩 → helm_release/secret/namespace 순서로
# depends_on이 걸려 자기완결적으로 풀린다.
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
