# 관리형 노드그룹은 시스템 컴포넌트(CoreDNS, Karpenter 컨트롤러 등)만 올리는 최소 구성
# (고정 2대, taint 없음). 매칭엔진/접수API/기록기 같은 backend 워크로드는 karpenter.tf의
# NodePool이 별도로 EC2를 띄워 담당한다.

data "aws_iam_policy_document" "eks_node_assume" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["ec2.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "team1_eks_node" {
  name               = "team1-eks-node-role"
  assume_role_policy = data.aws_iam_policy_document.eks_node_assume.json

  tags = {
    Team = "team1"
    Name = "team1-eks-node-role"
  }
}

resource "aws_iam_role_policy_attachment" "eks_node_worker" {
  role       = aws_iam_role.team1_eks_node.name
  policy_arn = "arn:aws:iam::aws:policy/AmazonEKSWorkerNodePolicy"
}

resource "aws_iam_role_policy_attachment" "eks_node_cni" {
  role       = aws_iam_role.team1_eks_node.name
  policy_arn = "arn:aws:iam::aws:policy/AmazonEKS_CNI_Policy"
}

resource "aws_iam_role_policy_attachment" "eks_node_ecr" {
  role       = aws_iam_role.team1_eks_node.name
  policy_arn = "arn:aws:iam::aws:policy/AmazonEC2ContainerRegistryReadOnly"
}

resource "aws_iam_instance_profile" "team1_eks_node" {
  name = "team1-eks-node-profile"
  role = aws_iam_role.team1_eks_node.name

  tags = {
    Team = "team1"
    Name = "team1-eks-node-profile"
  }
}

resource "aws_eks_access_entry" "eks_node" {
  cluster_name  = aws_eks_cluster.team1.name
  principal_arn = aws_iam_role.team1_eks_node.arn
  type          = "EC2_LINUX"
}

# vpc_security_group_ids를 지정하면 EKS 자동생성 SG를 대체한다(추가 아님).
resource "aws_launch_template" "eks_node_system" {
  name = "team1-eks-node-system"

  vpc_security_group_ids = [data.terraform_remote_state.network.outputs.security_group_ids.eks_backend]

  metadata_options {
    http_tokens = "required"
  }

  tag_specifications {
    resource_type = "instance"
    tags = {
      Team = "team1"
      Name = "team1-eks-node-system"
    }
  }

  tags = {
    Team = "team1"
    Name = "team1-eks-node-system-lt"
  }
}

resource "aws_eks_node_group" "system" {
  cluster_name    = aws_eks_cluster.team1.name
  node_group_name = "team1-ng-system"
  node_role_arn   = aws_iam_role.team1_eks_node.arn
  instance_types  = ["t3.medium"]

  subnet_ids = [
    data.terraform_remote_state.network.outputs.subnet_ids.eks_backend.a,
    data.terraform_remote_state.network.outputs.subnet_ids.eks_backend.b,
  ]

  launch_template {
    id = aws_launch_template.eks_node_system.id
    # "$Latest" 리터럴을 쓰면 AWS가 반환하는 해석된 버전 번호와 매번 drift가 난다.
    version = aws_launch_template.eks_node_system.latest_version
  }

  scaling_config {
    min_size     = 2
    desired_size = 2
    max_size     = 2
  }

  depends_on = [
    aws_iam_role_policy_attachment.eks_node_worker,
    aws_iam_role_policy_attachment.eks_node_cni,
    aws_iam_role_policy_attachment.eks_node_ecr,
    aws_eks_access_entry.eks_node,
  ]

  tags = {
    Team = "team1"
    Name = "team1-ng-system"
  }
}
