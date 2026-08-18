# AWS Load Balancer Controller — Public ALB(접수 API)는 컨트롤러가 Ingress 적용 시 생성한다
# (K8s 레이어, Terraform 밖). 여기선 컨트롤러용 IRSA와 서브넷 자동탐색 태그만 만든다.
# policies/alb_controller_policy.json은 AWS 공식 배포본 원본 — 적용 전 최신본 확인 권장.

resource "aws_iam_policy" "alb_controller" {
  name   = "team1-alb-controller-policy"
  policy = file("${path.module}/policies/alb_controller_policy.json")
}

data "aws_iam_policy_document" "irsa_alb_controller_assume" {
  statement {
    actions = ["sts:AssumeRoleWithWebIdentity"]
    principals {
      type        = "Federated"
      identifiers = [aws_iam_openid_connect_provider.team1_eks.arn]
    }
    condition {
      test     = "StringEquals"
      variable = "${local.eks_oidc_url_no_scheme}:sub"
      values   = ["system:serviceaccount:kube-system:aws-load-balancer-controller"]
    }
    condition {
      test     = "StringEquals"
      variable = "${local.eks_oidc_url_no_scheme}:aud"
      values   = ["sts.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "alb_controller" {
  name               = "team1-alb-controller-role"
  assume_role_policy = data.aws_iam_policy_document.irsa_alb_controller_assume.json

  tags = {
    Team = "team1"
    Name = "team1-alb-controller-role"
  }
}

resource "aws_iam_role_policy_attachment" "alb_controller" {
  role       = aws_iam_role.alb_controller.name
  policy_arn = aws_iam_policy.alb_controller.arn
}

# ALB Controller 서브넷 자동 탐색용 태그. 접수 API는 Public ALB로 노출되므로 role/elb은
# 퍼블릭 서브넷에 붙인다.
resource "aws_ec2_tag" "public_elb_a" {
  resource_id = data.terraform_remote_state.network.outputs.subnet_ids.public.a
  key         = "kubernetes.io/role/elb"
  value       = "1"
}

resource "aws_ec2_tag" "public_elb_b" {
  resource_id = data.terraform_remote_state.network.outputs.subnet_ids.public.b
  key         = "kubernetes.io/role/elb"
  value       = "1"
}

resource "aws_ec2_tag" "public_cluster_shared_a" {
  resource_id = data.terraform_remote_state.network.outputs.subnet_ids.public.a
  key         = "kubernetes.io/cluster/${aws_eks_cluster.team1.name}"
  value       = "shared"
}

resource "aws_ec2_tag" "public_cluster_shared_b" {
  resource_id = data.terraform_remote_state.network.outputs.subnet_ids.public.b
  key         = "kubernetes.io/cluster/${aws_eks_cluster.team1.name}"
  value       = "shared"
}

resource "aws_ec2_tag" "backend_cluster_shared_a" {
  resource_id = data.terraform_remote_state.network.outputs.subnet_ids.eks_backend.a
  key         = "kubernetes.io/cluster/${aws_eks_cluster.team1.name}"
  value       = "shared"
}

resource "aws_ec2_tag" "backend_cluster_shared_b" {
  resource_id = data.terraform_remote_state.network.outputs.subnet_ids.eks_backend.b
  key         = "kubernetes.io/cluster/${aws_eks_cluster.team1.name}"
  value       = "shared"
}
