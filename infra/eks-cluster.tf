# 단일 클러스터 안에서 노드그룹(backend)과 Fargate Profile(collector/aitrader/replay)로
# 워크로드를 나눈다. 인증 모드는 API 전용 — 노드/Karpenter/Job트리거 Lambda는 액세스
# 엔트리로 등록한다.

variable "eks_cluster_version" {
  description = "EKS 컨트롤 플레인 버전"
  type        = string
  default     = "1.36"
}

data "aws_iam_policy_document" "eks_cluster_assume" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["eks.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "team1_eks_cluster" {
  name               = "team1-eks-cluster-role"
  assume_role_policy = data.aws_iam_policy_document.eks_cluster_assume.json

  tags = {
    Team = "team1"
    Name = "team1-eks-cluster-role"
  }
}

resource "aws_iam_role_policy_attachment" "team1_eks_cluster" {
  role       = aws_iam_role.team1_eks_cluster.name
  policy_arn = "arn:aws:iam::aws:policy/AmazonEKSClusterPolicy"
}

resource "aws_eks_cluster" "team1" {
  name     = "team1-eks"
  role_arn = aws_iam_role.team1_eks_cluster.arn
  version  = var.eks_cluster_version

  vpc_config {
    subnet_ids = concat(
      [
        data.terraform_remote_state.network.outputs.subnet_ids.eks_backend.a,
        data.terraform_remote_state.network.outputs.subnet_ids.eks_backend.b,
      ],
      [
        data.terraform_remote_state.network.outputs.subnet_ids.eks_collector.a,
        data.terraform_remote_state.network.outputs.subnet_ids.eks_collector.b,
        data.terraform_remote_state.network.outputs.subnet_ids.eks_aitrader.a,
        data.terraform_remote_state.network.outputs.subnet_ids.eks_aitrader.b,
        data.terraform_remote_state.network.outputs.subnet_ids.eks_replay.a,
        data.terraform_remote_state.network.outputs.subnet_ids.eks_replay.b,
      ],
    )
    security_group_ids      = [data.terraform_remote_state.network.outputs.security_group_ids.eks_cluster]
    endpoint_private_access = true
    endpoint_public_access  = true
  }

  access_config {
    authentication_mode                         = "API"
    bootstrap_cluster_creator_admin_permissions = true
  }

  enabled_cluster_log_types = ["api", "audit", "authenticator", "controllerManager", "scheduler"]

  tags = {
    Team = "team1"
    Name = "team1-eks"
  }

  # 로그 그룹을 먼저 만들어야 한다 — 순서가 바뀌면 EKS가 기본 보관기간으로 그룹을
  # 자체 생성해버려 monitoring.tf의 retention 설정과 충돌한다.
  depends_on = [
    aws_iam_role_policy_attachment.team1_eks_cluster,
  ]
}

data "tls_certificate" "team1_eks" {
  url = aws_eks_cluster.team1.identity[0].oidc[0].issuer
}

resource "aws_iam_openid_connect_provider" "team1_eks" {
  url             = aws_eks_cluster.team1.identity[0].oidc[0].issuer
  client_id_list  = ["sts.amazonaws.com"]
  thumbprint_list = [data.tls_certificate.team1_eks.certificates[0].sha1_fingerprint]

  tags = {
    Team = "team1"
    Name = "team1-eks-oidc"
  }
}

locals {
  eks_oidc_url_no_scheme = replace(aws_iam_openid_connect_provider.team1_eks.url, "https://", "")
}
