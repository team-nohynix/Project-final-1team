# 전 계층 2 AZ(a/b). 단일 EKS 클러스터 안에서 노드그룹(backend)과
# Fargate Profile 3종(collector/aitrader/replay)이 이 서브넷 경계를 워크로드 격리 단위로 쓴다.

resource "aws_vpc" "team1_vpc" {
  cidr_block           = "10.10.0.0/16"
  enable_dns_support   = true
  enable_dns_hostnames = true

  tags = {
    Team = "team1"
    Name = "team1-vpc"
  }
}

resource "aws_internet_gateway" "team1_igw" {
  vpc_id = aws_vpc.team1_vpc.id

  tags = {
    Team = "team1"
    Name = "team1-igw"
  }
}

# 퍼블릭 서브넷 — NAT + 접수 API용 Public ALB(외부 노출, WAF 연결).

resource "aws_subnet" "team1_public_a" {
  vpc_id                  = aws_vpc.team1_vpc.id
  cidr_block              = "10.10.0.0/24"
  availability_zone       = "ap-northeast-2a"
  map_public_ip_on_launch = true

  tags = {
    Team = "team1"
    Name = "team1-public-a"
  }

  # root 스택(alb-controller.tf)이 aws_ec2_tag로 kubernetes.io/role/elb 태그를 추가하므로
  # 여기서 drift로 지우지 않는다.
  lifecycle {
    ignore_changes = [tags, tags_all]
  }
}

resource "aws_subnet" "team1_public_b" {
  vpc_id                  = aws_vpc.team1_vpc.id
  cidr_block              = "10.10.1.0/24"
  availability_zone       = "ap-northeast-2b"
  map_public_ip_on_launch = true

  tags = {
    Team = "team1"
    Name = "team1-public-b"
  }

  lifecycle {
    ignore_changes = [tags, tags_all]
  }
}

# EKS 백엔드 — 관리형 노드그룹(시스템 컴포넌트+Karpenter 컨트롤러) + Karpenter가 띄우는
# backend NodePool 노드(매칭엔진/접수API/기록기)가 모두 이 서브넷을 쓴다.

resource "aws_subnet" "team1_eks_backend_a" {
  vpc_id            = aws_vpc.team1_vpc.id
  cidr_block        = "10.10.10.0/24"
  availability_zone = "ap-northeast-2a"

  tags = {
    Team = "team1"
    Name = "team1-eks-backend-a"
  }

  lifecycle {
    ignore_changes = [tags, tags_all]
  }
}

resource "aws_subnet" "team1_eks_backend_b" {
  vpc_id            = aws_vpc.team1_vpc.id
  cidr_block        = "10.10.11.0/24"
  availability_zone = "ap-northeast-2b"

  tags = {
    Team = "team1"
    Name = "team1-eks-backend-b"
  }

  lifecycle {
    ignore_changes = [tags, tags_all]
  }
}

# EKS 시세 수집기 (Fargate Profile, a/b만)

resource "aws_subnet" "team1_eks_collector_a" {
  vpc_id            = aws_vpc.team1_vpc.id
  cidr_block        = "10.10.20.0/24"
  availability_zone = "ap-northeast-2a"

  tags = {
    Team = "team1"
    Name = "team1-eks-collector-a"
  }
}

resource "aws_subnet" "team1_eks_collector_b" {
  vpc_id            = aws_vpc.team1_vpc.id
  cidr_block        = "10.10.21.0/24"
  availability_zone = "ap-northeast-2b"

  tags = {
    Team = "team1"
    Name = "team1-eks-collector-b"
  }
}

# EKS AI 트레이더 (Fargate Profile, a/b만)

resource "aws_subnet" "team1_eks_aitrader_a" {
  vpc_id            = aws_vpc.team1_vpc.id
  cidr_block        = "10.10.30.0/24"
  availability_zone = "ap-northeast-2a"

  tags = {
    Team = "team1"
    Name = "team1-eks-aitrader-a"
  }
}

resource "aws_subnet" "team1_eks_aitrader_b" {
  vpc_id            = aws_vpc.team1_vpc.id
  cidr_block        = "10.10.31.0/24"
  availability_zone = "ap-northeast-2b"

  tags = {
    Team = "team1"
    Name = "team1-eks-aitrader-b"
  }
}

# EKS 리플레이 엔진 (Fargate Profile, a/b만)

resource "aws_subnet" "team1_eks_replay_a" {
  vpc_id            = aws_vpc.team1_vpc.id
  cidr_block        = "10.10.40.0/24"
  availability_zone = "ap-northeast-2a"

  tags = {
    Team = "team1"
    Name = "team1-eks-replay-a"
  }
}

resource "aws_subnet" "team1_eks_replay_b" {
  vpc_id            = aws_vpc.team1_vpc.id
  cidr_block        = "10.10.41.0/24"
  availability_zone = "ap-northeast-2b"

  tags = {
    Team = "team1"
    Name = "team1-eks-replay-b"
  }
}

# 데이터 계층 (ElastiCache/MSK, a/b 2 AZ) — 원래 RDS 3번째 노드용으로 2d에
# team1-data-d(10.10.52.0/24)가 있었으나, RDS를 자체 호스팅 MySQL EC2(mysql-ec2.tf)로
# 전환(rds.tf 참고)하면서 그 서브넷을 쓰는 리소스가 하나도 안 남아 통째로 제거했다.

resource "aws_subnet" "team1_data_a" {
  vpc_id            = aws_vpc.team1_vpc.id
  cidr_block        = "10.10.50.0/24"
  availability_zone = "ap-northeast-2a"

  tags = {
    Team = "team1"
    Name = "team1-data-a"
  }
}

resource "aws_subnet" "team1_data_b" {
  vpc_id            = aws_vpc.team1_vpc.id
  cidr_block        = "10.10.51.0/24"
  availability_zone = "ap-northeast-2b"

  tags = {
    Team = "team1"
    Name = "team1-data-b"
  }
}
