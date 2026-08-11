# S3 Gateway Endpoint(무료) + Interface Endpoint 6종. SQS는 접수API -> Job 트리거 큐 발행,
# job-trigger Lambda -> 큐 소비 양쪽에 필요해 추가.

resource "aws_vpc_endpoint" "team1_s3" {
  vpc_id            = aws_vpc.team1_vpc.id
  service_name      = "com.amazonaws.ap-northeast-2.s3"
  vpc_endpoint_type = "Gateway"

  route_table_ids = [
    aws_route_table.team1_private_a.id,
    aws_route_table.team1_private_b.id,
  ]

  tags = {
    Team = "team1"
    Name = "team1-vpce-s3"
  }
}

# ENI는 eks-backend-a/b에만 두고(Job 트리거 Lambda도 같은 서브넷에 배치), 다른 컴퓨트
# 서브넷(collector/aitrader/replay Fargate)은 프라이빗 DNS로 공유 접근한다.
locals {
  team1_interface_endpoints = {
    ecr_api         = "com.amazonaws.ap-northeast-2.ecr.api"
    ecr_dkr         = "com.amazonaws.ap-northeast-2.ecr.dkr"
    logs            = "com.amazonaws.ap-northeast-2.logs"
    sts             = "com.amazonaws.ap-northeast-2.sts"
    bedrock_runtime = "com.amazonaws.ap-northeast-2.bedrock-runtime"
    sqs             = "com.amazonaws.ap-northeast-2.sqs"
  }
}

resource "aws_vpc_endpoint" "team1_interface" {
  for_each = local.team1_interface_endpoints

  vpc_id              = aws_vpc.team1_vpc.id
  service_name        = each.value
  vpc_endpoint_type   = "Interface"
  private_dns_enabled = true

  subnet_ids = [
    aws_subnet.team1_eks_backend_a.id,
    aws_subnet.team1_eks_backend_b.id,
  ]

  security_group_ids = [aws_security_group.team1_sg_vpc_endpoints.id]

  tags = {
    Team = "team1"
    Name = "team1-vpce-${each.key}"
  }
}
