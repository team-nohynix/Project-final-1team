# 시세수집기/AI트레이더/리플레이 — 네임스페이스 셀렉터로 격리되는 Fargate Profile 3개.
# kube-system은 team1-ng-system(EC2)에서 상시 실행되므로 여기 셀렉터에 넣지 않는다.

# Fargate 파드는 network 스택에서 만든 team1_sg_eks_cluster가 아니라 EKS가 자동
# 생성하는 클러스터 SG(vpc_config[0].cluster_security_group_id)를 쓴다. 이 SG는
# root 스택 리소스(aws_eks_cluster.team1)라 여기서 바로 참조 가능 — network 스택
# 쪽처럼 순환 의존 문제가 없다.
resource "aws_security_group_rule" "team1_collector_from_alb_real_sg" {
  type                     = "ingress"
  security_group_id        = aws_eks_cluster.team1.vpc_config[0].cluster_security_group_id
  source_security_group_id = data.terraform_remote_state.network.outputs.security_group_ids.alb_public
  from_port                = 8080
  to_port                  = 8080
  protocol                 = "tcp"
  description              = "Public ALB to market-data collector Fargate pod (EKS auto-created cluster SG, not our own)"
}

# CoreDNS는 백엔드 노드그룹(EC2, SG=team1_sg_eks_backend)에서 도는데, network 스택의
# team1_cluster_to_backend 규칙은 우리가 만든(안 쓰이는) team1_sg_eks_cluster만
# 허용한다. Fargate 파드가 실제로 쓰는 위 자동생성 SG를 허용해야 Fargate 파드의
# DNS 조회가 CoreDNS에 도달한다 — 컨트롤플레인<->노드 규칙과 같은 포트 범위(0-65535/all).
resource "aws_security_group_rule" "team1_backend_from_fargate_real_sg" {
  type                     = "ingress"
  security_group_id        = data.terraform_remote_state.network.outputs.security_group_ids.eks_backend
  source_security_group_id = aws_eks_cluster.team1.vpc_config[0].cluster_security_group_id
  from_port                = 0
  to_port                  = 0
  protocol                 = "-1"
  description              = "Fargate pods (real EKS auto-created cluster SG) to backend node group (CoreDNS etc.)"
}

# 반대 방향: orderapi(team1_sg_eks_backend)가 시세 수집기를 대시보드 헬스체크용으로
# http://backend.collector:8080/...로 직접 호출한다 — ALB→Fargate 규칙만으로는
# backend 노드그룹→Fargate 경로가 안 열려 있었다.
resource "aws_security_group_rule" "team1_fargate_from_backend_real_sg" {
  type                     = "ingress"
  security_group_id        = aws_eks_cluster.team1.vpc_config[0].cluster_security_group_id
  source_security_group_id = data.terraform_remote_state.network.outputs.security_group_ids.eks_backend
  from_port                = 8080
  to_port                  = 8080
  protocol                 = "tcp"
  description              = "Backend node group (orderapi dashboard health-check) to market-data collector Fargate pod"
}

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
