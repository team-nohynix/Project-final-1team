# EKS 관리형 애드온. resolve_conflicts_on_create=OVERWRITE인 이유: EKS가 클러스터 생성 시
# vpc-cni/coredns/kube-proxy를 자체관리(self-managed) 버전으로 이미 깔아두므로, Terraform
# 관리 addon으로 전환할 때 충돌을 덮어써야 한다. Pod Identity 애드온은 넣지 않는다 — IRSA로
# 통일해서 쓴다.

locals {
  team1_eks_core_addons = {
    vpc-cni    = {}
    coredns    = {}
    kube-proxy = {}
  }
}

resource "aws_eks_addon" "core" {
  for_each = local.team1_eks_core_addons

  cluster_name                = aws_eks_cluster.team1.name
  addon_name                  = each.key
  resolve_conflicts_on_create = "OVERWRITE"
  resolve_conflicts_on_update = "OVERWRITE"

  tags = {
    Team = "team1"
    Name = "team1-eks-addon-${each.key}"
  }

  # coredns는 스케줄될 노드가 있어야 Active로 전환된다.
  depends_on = [aws_eks_node_group.system]
}

# amazon-cloudwatch-observability 애드온의 기본 서비스 어카운트(amazon-cloudwatch 네임스페이스/
# cloudwatch-agent)용 IRSA.
data "aws_iam_policy_document" "irsa_cloudwatch_agent_assume" {
  statement {
    actions = ["sts:AssumeRoleWithWebIdentity"]
    principals {
      type        = "Federated"
      identifiers = [aws_iam_openid_connect_provider.team1_eks.arn]
    }
    condition {
      test     = "StringEquals"
      variable = "${local.eks_oidc_url_no_scheme}:sub"
      values   = ["system:serviceaccount:amazon-cloudwatch:cloudwatch-agent"]
    }
    condition {
      test     = "StringEquals"
      variable = "${local.eks_oidc_url_no_scheme}:aud"
      values   = ["sts.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "cloudwatch_agent" {
  name               = "team1-cloudwatch-agent-role"
  assume_role_policy = data.aws_iam_policy_document.irsa_cloudwatch_agent_assume.json

  tags = {
    Team = "team1"
    Name = "team1-cloudwatch-agent-role"
  }
}

resource "aws_iam_role_policy_attachment" "cloudwatch_agent" {
  role       = aws_iam_role.cloudwatch_agent.name
  policy_arn = "arn:aws:iam::aws:policy/CloudWatchAgentServerPolicy"
}

resource "aws_eks_addon" "cloudwatch_observability" {
  cluster_name                = aws_eks_cluster.team1.name
  addon_name                  = "amazon-cloudwatch-observability"
  service_account_role_arn    = aws_iam_role.cloudwatch_agent.arn
  resolve_conflicts_on_create = "OVERWRITE"
  resolve_conflicts_on_update = "OVERWRITE"

  tags = {
    Team = "team1"
    Name = "team1-eks-addon-cloudwatch-observability"
  }

  depends_on = [aws_eks_node_group.system]
}
