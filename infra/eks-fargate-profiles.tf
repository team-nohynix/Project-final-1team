# 시세수집기/AI트레이더/리플레이 — 네임스페이스 셀렉터로 격리되는 Fargate Profile 3개.
# kube-system은 team1-ng-system(EC2)에서 상시 실행되므로 여기 셀렉터에 넣지 않는다.

# Fargate 파드는 network 스택에서 만든 team1_sg_eks_cluster가 아니라 EKS가 자동
# 생성하는 클러스터 SG(vpc_config[0].cluster_security_group_id)를 쓴다 — ALB에서
# 시세 수집기(collector)로 인그레스를 열 때 이걸 실제로 놓쳐서 헬스체크가
# Target.Timeout으로 실패하는 걸 라이브로 확인했다(VPC 엔드포인트 때 겪은 것과
# 같은 함정). 이 SG는 root 스택 리소스(aws_eks_cluster.team1)라 여기서 바로 참조
# 가능 — network 스택 쪽 VPC 엔드포인트 때처럼 순환 의존 문제가 없다.
resource "aws_security_group_rule" "team1_collector_from_alb_real_sg" {
  type                     = "ingress"
  security_group_id        = aws_eks_cluster.team1.vpc_config[0].cluster_security_group_id
  source_security_group_id = data.terraform_remote_state.network.outputs.security_group_ids.alb_public
  from_port                = 8080
  to_port                  = 8080
  protocol                 = "tcp"
  description              = "Public ALB to market-data collector Fargate pod (EKS auto-created cluster SG, not our own)"
}

# 같은 함정, 세 번째로 발견: CoreDNS는 백엔드 노드그룹(EC2, SG=team1_sg_eks_backend —
# 런치 템플릿이 자동생성 SG를 대체함)에서 도는데, network 스택의
# team1_cluster_to_backend 규칙은 우리가 만든 (안 쓰이는) team1_sg_eks_cluster만
# 허용한다. Fargate 파드(시세 수집기 등)가 실제로 쓰는 이 진짜 자동생성 SG가
# 빠져 있어서 Fargate 파드의 모든 DNS 조회(api.upbit.com 포함)가 CoreDNS 자체에
# 도달을 못 해 i/o timeout으로 죽는 걸 라이브로 확인했다(시세 수집기가 몇 분째
# 응답 없이 걸려있던 진짜 원인). 기존 컨트롤플레인<->노드 규칙과 같은 포트 범위로
# 맞춘다(0-65535/all, DNS만 열 이유가 없음 — Fargate도 결국 클러스터 SG의 다른
# 용도를 공유하는 게 정상 설계).
resource "aws_security_group_rule" "team1_backend_from_fargate_real_sg" {
  type                     = "ingress"
  security_group_id        = data.terraform_remote_state.network.outputs.security_group_ids.eks_backend
  source_security_group_id = aws_eks_cluster.team1.vpc_config[0].cluster_security_group_id
  from_port                = 0
  to_port                  = 0
  protocol                 = "-1"
  description              = "Fargate pods (real EKS auto-created cluster SG) to backend node group (CoreDNS etc.)"
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
