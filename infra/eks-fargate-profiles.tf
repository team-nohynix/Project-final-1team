# 시세수집기/AI트레이더/리플레이 — 네임스페이스 셀렉터로 격리되는 Fargate Profile 3개.
# kube-system은 team1-ng-system(EC2)에서 상시 실행되므로 여기 셀렉터에 넣지 않는다.

data "aws_iam_policy_document" "fargate_pod_execution_assume" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["eks-fargate-pods.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "fargate_pod_execution" {
  name               = "team1-fargate-pod-execution"
  assume_role_policy = data.aws_iam_policy_document.fargate_pod_execution_assume.json

  tags = {
    Team = "team1"
    Name = "team1-fargate-pod-execution"
  }
}

resource "aws_iam_role_policy_attachment" "fargate_pod_execution" {
  role       = aws_iam_role.fargate_pod_execution.name
  policy_arn = "arn:aws:iam::aws:policy/AmazonEKSFargatePodExecutionRolePolicy"
}

locals {
  team1_fargate_profiles = {
    collector = {
      namespace = "collector"
      subnet_ids = [
        data.terraform_remote_state.network.outputs.subnet_ids.eks_collector.a,
        data.terraform_remote_state.network.outputs.subnet_ids.eks_collector.b,
      ]
    }
    aitrader = {
      namespace = "ai-trader"
      subnet_ids = [
        data.terraform_remote_state.network.outputs.subnet_ids.eks_aitrader.a,
        data.terraform_remote_state.network.outputs.subnet_ids.eks_aitrader.b,
      ]
    }
    replay = {
      namespace = "replay"
      subnet_ids = [
        data.terraform_remote_state.network.outputs.subnet_ids.eks_replay.a,
        data.terraform_remote_state.network.outputs.subnet_ids.eks_replay.b,
      ]
    }
  }
}

resource "aws_eks_fargate_profile" "this" {
  for_each = local.team1_fargate_profiles

  cluster_name           = aws_eks_cluster.team1.name
  fargate_profile_name   = "team1-fp-${each.key}"
  pod_execution_role_arn = aws_iam_role.fargate_pod_execution.arn
  subnet_ids             = each.value.subnet_ids

  selector {
    namespace = each.value.namespace
  }

  tags = {
    Team = "team1"
    Name = "team1-fp-${each.key}"
  }

  depends_on = [aws_iam_role_policy_attachment.fargate_pod_execution]
}
