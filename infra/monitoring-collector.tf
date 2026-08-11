# 클러스터 안에서 도는 Prometheus(kube-prometheus-stack)가 AMP로 remote_write할 때 쓰는
# IRSA. AMP는 SigV4 서명을 요구해서 액세스 키 없이 IAM Role로만 인증한다.

data "aws_iam_policy_document" "irsa_prometheus_assume" {
  statement {
    actions = ["sts:AssumeRoleWithWebIdentity"]
    principals {
      type        = "Federated"
      identifiers = [aws_iam_openid_connect_provider.team1_eks.arn]
    }
    condition {
      test     = "StringEquals"
      variable = "${local.eks_oidc_url_no_scheme}:sub"
      values   = ["system:serviceaccount:monitoring:prometheus"]
    }
    condition {
      test     = "StringEquals"
      variable = "${local.eks_oidc_url_no_scheme}:aud"
      values   = ["sts.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "prometheus" {
  name               = "team1-prometheus-role"
  assume_role_policy = data.aws_iam_policy_document.irsa_prometheus_assume.json

  tags = {
    Team = "team1"
    Name = "team1-prometheus-role"
  }
}

data "aws_iam_policy_document" "prometheus_policy" {
  statement {
    actions   = ["aps:RemoteWrite", "aps:GetSeries", "aps:GetLabels", "aps:GetMetricMetadata"]
    resources = [aws_prometheus_workspace.team1.arn]
  }
}

resource "aws_iam_role_policy" "prometheus" {
  name   = "team1-prometheus-policy"
  role   = aws_iam_role.prometheus.id
  policy = data.aws_iam_policy_document.prometheus_policy.json
}

output "prometheus_irsa_role_arn" {
  value = aws_iam_role.prometheus.arn
}
