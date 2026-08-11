# Fargate 파드는 프로파일별 개별 보안 그룹을 가질 수 없다(Security Groups for Pods는
# EC2/Nitro 노드 전용) — 클러스터에 설정한 SG를 전부 공유한다.
#   - team1_sg_eks_cluster: 클러스터 vpc_config에 지정 → 컨트롤플레인 + Fargate 파드 전부 공유
#   - team1_sg_eks_backend: 관리형 노드그룹 런치 템플릿에서 자동생성 SG를 대체(추가 아님) →
#     MSK/RDS/Redis 인그레스는 이 SG만 허용해, Kafka를 쓰지 않는 collector/ai-trader Fargate
#     파드는 애초에 접근 권한이 없다(격리는 IRSA+SG 이중)
# description은 ASCII만 허용돼 영문으로 쓴다.

resource "aws_security_group" "team1_sg_eks_cluster" {
  name        = "team1-sg-eks-cluster"
  description = "team1 EKS control plane + Fargate pods (collector/ai-trader/replay share this, Fargate has no per-profile SG)"
  vpc_id      = aws_vpc.team1_vpc.id

  egress {
    description = "allow all outbound"
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }

  tags = {
    Team = "team1"
    Name = "team1-sg-eks-cluster"
  }
}

resource "aws_security_group_rule" "team1_sg_eks_cluster_self" {
  type              = "ingress"
  security_group_id = aws_security_group.team1_sg_eks_cluster.id
  self              = true
  from_port         = 0
  to_port           = 0
  protocol          = "-1"
  description       = "control plane to Fargate pod / pod to pod (self)"
}

resource "aws_security_group" "team1_sg_eks_backend" {
  name        = "team1-sg-eks-backend"
  description = "team1 backend node group (ingest API, matching engine, recorder) - EC2, launch template overrides cluster SG"
  vpc_id      = aws_vpc.team1_vpc.id

  egress {
    description = "allow all outbound"
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }

  tags = {
    Team = "team1"
    Name = "team1-sg-eks-backend"
  }

  # root 스택(karpenter.tf)이 aws_ec2_tag로 karpenter.sh/discovery 태그를 추가한다 — 여기서
  # drift로 지우면 EC2NodeClass의 securityGroupSelectorTerms가 이 SG를 못 찾게 된다.
  lifecycle {
    ignore_changes = [tags, tags_all]
  }
}

resource "aws_security_group_rule" "team1_sg_eks_backend_self" {
  type              = "ingress"
  security_group_id = aws_security_group.team1_sg_eks_backend.id
  self              = true
  from_port         = 0
  to_port           = 0
  protocol          = "-1"
  description       = "node to node / pod to pod (self)"
}

# 관리형 노드그룹 런치 템플릿이 자동생성 클러스터 SG를 대체하므로(추가 아님), 컨트롤플레인
# <-> 노드 통신 경로를 양방향으로 명시해야 kubelet이 API 서버에 조인할 수 있다.
resource "aws_security_group_rule" "team1_cluster_to_backend" {
  type                     = "ingress"
  security_group_id        = aws_security_group.team1_sg_eks_backend.id
  source_security_group_id = aws_security_group.team1_sg_eks_cluster.id
  from_port                = 0
  to_port                  = 0
  protocol                 = "-1"
  description              = "EKS control plane to backend node group (kubelet/API)"
}

resource "aws_security_group_rule" "team1_backend_to_cluster" {
  type                     = "ingress"
  security_group_id        = aws_security_group.team1_sg_eks_cluster.id
  source_security_group_id = aws_security_group.team1_sg_eks_backend.id
  from_port                = 0
  to_port                  = 0
  protocol                 = "-1"
  description              = "backend node group to EKS control plane"
}

resource "aws_security_group" "team1_sg_msk" {
  name        = "team1-sg-msk"
  description = "team1 MSK Serverless - backend node group only (ingest API publish, matching engine subscribe/publish, recorder subscribe)"
  vpc_id      = aws_vpc.team1_vpc.id

  ingress {
    description     = "Kafka IAM(SASL) from backend node group"
    from_port       = 9098
    to_port         = 9098
    protocol        = "tcp"
    security_groups = [aws_security_group.team1_sg_eks_backend.id]
  }

  egress {
    description = "allow all outbound"
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }

  tags = {
    Team = "team1"
    Name = "team1-sg-msk"
  }
}

resource "aws_security_group" "team1_sg_rds" {
  name = "team1-sg-rds"
  # description은 SG를 ForceNew(전체 교체)시키는 필드라 엔진명이 바뀌어도 여기선 안 건드린다
  # — RDS가 이 SG에 이미 붙어 있으면 ENI 분리 권한 문제로 교체 자체가 실패한다.
  description = "team1 RDS (PostgreSQL) - backend node group (recorder) only"
  vpc_id      = aws_vpc.team1_vpc.id

  ingress {
    description     = "recorder to RDS write"
    from_port       = 3306
    to_port         = 3306
    protocol        = "tcp"
    security_groups = [aws_security_group.team1_sg_eks_backend.id]
  }

  egress {
    description = "allow all outbound"
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }

  tags = {
    Team = "team1"
    Name = "team1-sg-rds"
  }
}

resource "aws_security_group" "team1_sg_redis" {
  name        = "team1-sg-redis"
  description = "team1 ElastiCache (Redis) - backend node group only"
  vpc_id      = aws_vpc.team1_vpc.id

  ingress {
    description     = "matching engine write, ingest API read"
    from_port       = 6379
    to_port         = 6379
    protocol        = "tcp"
    security_groups = [aws_security_group.team1_sg_eks_backend.id]
  }

  egress {
    description = "allow all outbound"
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }

  tags = {
    Team = "team1"
    Name = "team1-sg-redis"
  }
}

# 접수 API는 Public ALB 뒤에서 외부 노출(AI트레이더/리플레이 Job은 클러스터 밖 세션
# 트리거이므로 여기를 거치지 않는다 - job-trigger.tf의 SQS+Lambda 경로 참고). WAF는
# 네트워크 홉이 아니라 ALB에 연결되는 규칙셋이라 root 스택에서 WebACL만 만들어 붙인다.
resource "aws_security_group" "team1_sg_alb_public" {
  name        = "team1-sg-alb-public"
  description = "team1 Public ALB - ingest API entrypoint (WAF associated in root stack)"
  vpc_id      = aws_vpc.team1_vpc.id

  ingress {
    description = "public HTTPS"
    from_port   = 443
    to_port     = 443
    protocol    = "tcp"
    cidr_blocks = ["0.0.0.0/0"]
  }

  ingress {
    description = "public HTTP (redirect to HTTPS at listener)"
    from_port   = 80
    to_port     = 80
    protocol    = "tcp"
    cidr_blocks = ["0.0.0.0/0"]
  }

  egress {
    description = "allow all outbound"
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }

  tags = {
    Team = "team1"
    Name = "team1-sg-alb-public"
  }
}

# 포트 8080은 설계 문서 11장의 placeholder — 접수 API 실제 포트 확정 시 갱신.
resource "aws_security_group_rule" "team1_backend_from_alb" {
  type                     = "ingress"
  security_group_id        = aws_security_group.team1_sg_eks_backend.id
  source_security_group_id = aws_security_group.team1_sg_alb_public.id
  from_port                = 8080
  to_port                  = 8080
  protocol                 = "tcp"
  description              = "Public ALB to ingest API pod (port is a placeholder, see design doc section 11)"
}

# Job 트리거 Lambda(team1-lambda-job-trigger, root 스택 job-trigger.tf) — SQS를 소비해
# EKS 프라이빗 엔드포인트를 호출한다. Lambda가 항상 발신 쪽이라 인그레스 규칙은 불필요.
resource "aws_security_group" "team1_sg_lambda_job_trigger" {
  name        = "team1-sg-lambda-job-trigger"
  description = "team1 Job trigger Lambda (VPC-attached) - consumes SQS, calls EKS private endpoint"
  vpc_id      = aws_vpc.team1_vpc.id

  egress {
    description = "allow all outbound"
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }

  tags = {
    Team = "team1"
    Name = "team1-sg-lambda-job-trigger"
  }
}

# Lambda가 EKS API(클러스터 프라이빗 엔드포인트)를 호출할 수 있어야 Job을 생성한다.
resource "aws_security_group_rule" "team1_cluster_from_lambda" {
  type                     = "ingress"
  security_group_id        = aws_security_group.team1_sg_eks_cluster.id
  source_security_group_id = aws_security_group.team1_sg_lambda_job_trigger.id
  from_port                = 443
  to_port                  = 443
  protocol                 = "tcp"
  description              = "Job trigger Lambda to EKS private endpoint"
}

resource "aws_security_group" "team1_sg_vpc_endpoints" {
  name        = "team1-sg-vpc-endpoints"
  description = "team1 Interface Endpoints (ECR, CloudWatch Logs, STS, Bedrock Runtime, SQS)"
  vpc_id      = aws_vpc.team1_vpc.id

  ingress {
    description = "backend node group + EKS cluster/Fargate + job-trigger Lambda to Interface Endpoint"
    from_port   = 443
    to_port     = 443
    protocol    = "tcp"
    security_groups = [
      aws_security_group.team1_sg_eks_backend.id,
      aws_security_group.team1_sg_eks_cluster.id,
      aws_security_group.team1_sg_lambda_job_trigger.id,
    ]
  }

  egress {
    description = "allow all outbound"
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }

  tags = {
    Team = "team1"
    Name = "team1-sg-vpc-endpoints"
  }
}
