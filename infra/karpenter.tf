# Karpenter — 컨트롤러 자체는 team1-ng-system 위에 Helm으로 설치한다. 여기서는 컨트롤러
# IRSA, Karpenter가 띄우는 노드용 IAM Role/Instance Profile, 스팟 중단 처리용 SQS+
# EventBridge만 만든다. NodePool/EC2NodeClass는 k8s/karpenter/에서 적용한다.

locals {
  karpenter_node_role_name  = "team1-karpenter-node-role"
  karpenter_discovery_value = "team1-eks"
}

# EC2NodeClass의 subnetSelectorTerms/securityGroupSelectorTerms가 찾는 태그.
resource "aws_ec2_tag" "karpenter_discovery_backend_a" {
  resource_id = data.terraform_remote_state.network.outputs.subnet_ids.eks_backend.a
  key         = "karpenter.sh/discovery"
  value       = local.karpenter_discovery_value
}

resource "aws_ec2_tag" "karpenter_discovery_backend_b" {
  resource_id = data.terraform_remote_state.network.outputs.subnet_ids.eks_backend.b
  key         = "karpenter.sh/discovery"
  value       = local.karpenter_discovery_value
}

resource "aws_ec2_tag" "karpenter_discovery_sg_backend" {
  resource_id = data.terraform_remote_state.network.outputs.security_group_ids.eks_backend
  key         = "karpenter.sh/discovery"
  value       = local.karpenter_discovery_value
}

# ---------------------------------------------------------------------------
# Karpenter가 launch하는 노드의 IAM Role — 일반 EKS 노드와 동일한 관리형 정책.

resource "aws_iam_role" "karpenter_node" {
  name               = local.karpenter_node_role_name
  assume_role_policy = data.aws_iam_policy_document.eks_node_assume.json

  tags = {
    Team = "team1"
    Name = local.karpenter_node_role_name
  }
}

resource "aws_iam_role_policy_attachment" "karpenter_node_worker" {
  role       = aws_iam_role.karpenter_node.name
  policy_arn = "arn:aws:iam::aws:policy/AmazonEKSWorkerNodePolicy"
}

resource "aws_iam_role_policy_attachment" "karpenter_node_cni" {
  role       = aws_iam_role.karpenter_node.name
  policy_arn = "arn:aws:iam::aws:policy/AmazonEKS_CNI_Policy"
}

resource "aws_iam_role_policy_attachment" "karpenter_node_ecr" {
  role       = aws_iam_role.karpenter_node.name
  policy_arn = "arn:aws:iam::aws:policy/AmazonEC2ContainerRegistryReadOnly"
}

resource "aws_iam_role_policy_attachment" "karpenter_node_ssm" {
  role       = aws_iam_role.karpenter_node.name
  policy_arn = "arn:aws:iam::aws:policy/AmazonSSMManagedInstanceCore"
}

resource "aws_iam_instance_profile" "karpenter_node" {
  name = "team1-karpenter-node-profile"
  role = aws_iam_role.karpenter_node.name

  tags = {
    Team = "team1"
    Name = "team1-karpenter-node-profile"
  }
}

resource "aws_eks_access_entry" "karpenter_node" {
  cluster_name  = aws_eks_cluster.team1.name
  principal_arn = aws_iam_role.karpenter_node.arn
  type          = "EC2_LINUX"
}

# ---------------------------------------------------------------------------
# 스팟 중단 처리 — Karpenter가 폴링하는 인터럽션 큐.

resource "aws_sqs_queue" "karpenter_interruption" {
  name                      = "team1-sqs-karpenter-interruption"
  message_retention_seconds = 300

  tags = {
    Team = "team1"
    Name = "team1-sqs-karpenter-interruption"
  }
}

data "aws_iam_policy_document" "karpenter_interruption_queue_policy" {
  statement {
    actions   = ["sqs:SendMessage"]
    resources = [aws_sqs_queue.karpenter_interruption.arn]
    principals {
      type        = "Service"
      identifiers = ["events.amazonaws.com", "sqs.amazonaws.com"]
    }
  }
}

resource "aws_sqs_queue_policy" "karpenter_interruption" {
  queue_url = aws_sqs_queue.karpenter_interruption.id
  policy    = data.aws_iam_policy_document.karpenter_interruption_queue_policy.json
}

locals {
  karpenter_interruption_events = {
    spot_interruption = "aws.ec2 EC2 Spot Instance Interruption Warning"
    rebalance         = "aws.ec2 EC2 Instance Rebalance Recommendation"
    instance_state    = "aws.ec2 EC2 Instance State-change Notification"
    scheduled_change  = "aws.health AWS Health Event"
  }
}

resource "aws_cloudwatch_event_rule" "karpenter_spot_interruption" {
  name = "team1-karpenter-spot-interruption"
  event_pattern = jsonencode({
    source      = ["aws.ec2"]
    detail-type = ["EC2 Spot Instance Interruption Warning"]
  })
}

resource "aws_cloudwatch_event_rule" "karpenter_rebalance" {
  name = "team1-karpenter-rebalance"
  event_pattern = jsonencode({
    source      = ["aws.ec2"]
    detail-type = ["EC2 Instance Rebalance Recommendation"]
  })
}

resource "aws_cloudwatch_event_rule" "karpenter_instance_state" {
  name = "team1-karpenter-instance-state"
  event_pattern = jsonencode({
    source      = ["aws.ec2"]
    detail-type = ["EC2 Instance State-change Notification"]
  })
}

resource "aws_cloudwatch_event_target" "karpenter_spot_interruption" {
  rule = aws_cloudwatch_event_rule.karpenter_spot_interruption.name
  arn  = aws_sqs_queue.karpenter_interruption.arn
}

resource "aws_cloudwatch_event_target" "karpenter_rebalance" {
  rule = aws_cloudwatch_event_rule.karpenter_rebalance.name
  arn  = aws_sqs_queue.karpenter_interruption.arn
}

resource "aws_cloudwatch_event_target" "karpenter_instance_state" {
  rule = aws_cloudwatch_event_rule.karpenter_instance_state.name
  arn  = aws_sqs_queue.karpenter_interruption.arn
}

# ---------------------------------------------------------------------------
# 컨트롤러 IRSA — team1-ng-system 위에서 도는 kube-system/karpenter 서비스 어카운트.

data "aws_iam_policy_document" "irsa_karpenter_assume" {
  statement {
    actions = ["sts:AssumeRoleWithWebIdentity"]
    principals {
      type        = "Federated"
      identifiers = [aws_iam_openid_connect_provider.team1_eks.arn]
    }
    condition {
      test     = "StringEquals"
      variable = "${local.eks_oidc_url_no_scheme}:sub"
      values   = ["system:serviceaccount:kube-system:karpenter"]
    }
    condition {
      test     = "StringEquals"
      variable = "${local.eks_oidc_url_no_scheme}:aud"
      values   = ["sts.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "karpenter_controller" {
  name               = "team1-karpenter-controller-role"
  assume_role_policy = data.aws_iam_policy_document.irsa_karpenter_assume.json

  tags = {
    Team = "team1"
    Name = "team1-karpenter-controller-role"
  }
}

data "aws_iam_policy_document" "karpenter_controller_policy" {
  statement {
    sid = "AllowScopedEC2InstanceActions"
    actions = [
      "ec2:RunInstances",
      "ec2:CreateFleet",
      "ec2:CreateLaunchTemplate",
      "ec2:CreateTags",
      "ec2:TerminateInstances",
      "ec2:DeleteLaunchTemplate",
    ]
    resources = ["*"]
  }

  statement {
    sid = "AllowScopedDescribe"
    actions = [
      "ec2:DescribeInstances",
      "ec2:DescribeLaunchTemplates",
      "ec2:DescribeSecurityGroups",
      "ec2:DescribeSubnets",
      "ec2:DescribeInstanceTypes",
      "ec2:DescribeInstanceTypeOfferings",
      "ec2:DescribeAvailabilityZones",
      "ec2:DescribeSpotPriceHistory",
      "ec2:DescribeImages",
    ]
    resources = ["*"]
  }

  statement {
    sid       = "AllowPricing"
    actions   = ["pricing:GetProducts"]
    resources = ["*"]
  }

  statement {
    sid       = "AllowSSMReadForAMI"
    actions   = ["ssm:GetParameter"]
    resources = ["arn:aws:ssm:ap-northeast-2::parameter/aws/service/*"]
  }

  statement {
    sid       = "AllowPassNodeRole"
    actions   = ["iam:PassRole"]
    resources = [aws_iam_role.karpenter_node.arn]
  }

  # instanceProfile을 정적 참조해도 내장 GC 리컨실러가 항상 iam:ListInstanceProfiles를
  # 호출한다 — Get만 주면 AccessDenied가 계속 쌓인다.
  statement {
    sid = "AllowInstanceProfileManagement"
    actions = [
      "iam:CreateInstanceProfile",
      "iam:DeleteInstanceProfile",
      "iam:AddRoleToInstanceProfile",
      "iam:RemoveRoleFromInstanceProfile",
      "iam:GetInstanceProfile",
      "iam:TagInstanceProfile",
    ]
    resources = ["arn:aws:iam::${data.aws_caller_identity.current.account_id}:instance-profile/karpenter/ap-northeast-2/team1-eks/*"]
  }

  statement {
    sid       = "AllowListInstanceProfiles"
    actions   = ["iam:ListInstanceProfiles"]
    resources = ["*"] # ListInstanceProfiles는 리소스 레벨 제한을 지원하지 않는다
  }

  statement {
    sid       = "AllowEKSClusterRead"
    actions   = ["eks:DescribeCluster"]
    resources = [aws_eks_cluster.team1.arn]
  }

  statement {
    sid = "AllowInterruptionQueueConsume"
    actions = [
      "sqs:DeleteMessage",
      "sqs:GetQueueUrl",
      "sqs:ReceiveMessage",
    ]
    resources = [aws_sqs_queue.karpenter_interruption.arn]
  }
}

resource "aws_iam_role_policy" "karpenter_controller" {
  name   = "team1-karpenter-controller-policy"
  role   = aws_iam_role.karpenter_controller.id
  policy = data.aws_iam_policy_document.karpenter_controller_policy.json
}

output "karpenter_node_role_arn" {
  description = "NodePool/EC2NodeClass에서 참조할 Karpenter 노드 IAM Role ARN"
  value       = aws_iam_role.karpenter_node.arn
}

output "karpenter_interruption_queue_name" {
  value = aws_sqs_queue.karpenter_interruption.name
}

# Karpenter 컨트롤러 자체 설치(helm_release, 2026-08-24 추가) — 원래
# `helm install karpenter oci://public.ecr.aws/karpenter/karpenter ...`로 손으로
# 설치돼 있던 것을 "EKS를 통째로 지웠다 올려도 원상복구되게" terraform으로 옮겼다.
# values는 `helm get values karpenter -n kube-system`으로 뽑은 실제 사용자 지정값
# 그대로(2026-08-24 확인) — clusterEndpoint는 매번 EKS 클러스터가 새로 만들어질 때마다
# 바뀌므로 하드코딩하지 않고 aws_eks_cluster 리소스 속성을 그대로 참조한다(이게 이
# 리소스를 helm-releases.tf가 아니라 karpenter.tf에 같이 둔 이유이기도 함 — 관련
# IAM/SQS 리소스와 나란히).
resource "helm_release" "karpenter" {
  name             = "karpenter"
  namespace        = "kube-system"
  repository       = "oci://public.ecr.aws/karpenter"
  chart            = "karpenter"
  version          = "1.14.0" # helm list -A로 확인한 실제 배포 버전(2026-08-24)
  create_namespace = false    # kube-system은 이미 있음

  values = [yamlencode({
    serviceAccount = {
      annotations = {
        "eks.amazonaws.com/role-arn" = aws_iam_role.karpenter_controller.arn
      }
    }
    settings = {
      clusterName       = aws_eks_cluster.team1.name
      clusterEndpoint   = aws_eks_cluster.team1.endpoint
      interruptionQueue = aws_sqs_queue.karpenter_interruption.name
    }
    controller = {
      resources = {
        requests = {
          cpu    = "200m"
          memory = "256Mi"
        }
      }
    }
  })]

  depends_on = [aws_eks_node_group.system]
}
