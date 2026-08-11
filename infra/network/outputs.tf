output "vpc_id" {
  value = aws_vpc.team1_vpc.id
}

output "subnet_ids" {
  description = "티어별 서브넷 id — data만 a/b/d, 나머지는 a/b. Job 트리거 Lambda는 eks_backend 서브넷을 공유한다."
  value = {
    public        = { a = aws_subnet.team1_public_a.id, b = aws_subnet.team1_public_b.id }
    eks_backend   = { a = aws_subnet.team1_eks_backend_a.id, b = aws_subnet.team1_eks_backend_b.id }
    eks_collector = { a = aws_subnet.team1_eks_collector_a.id, b = aws_subnet.team1_eks_collector_b.id }
    eks_aitrader  = { a = aws_subnet.team1_eks_aitrader_a.id, b = aws_subnet.team1_eks_aitrader_b.id }
    eks_replay    = { a = aws_subnet.team1_eks_replay_a.id, b = aws_subnet.team1_eks_replay_b.id }
    data          = { a = aws_subnet.team1_data_a.id, b = aws_subnet.team1_data_b.id, d = aws_subnet.team1_data_d.id }
  }
}

output "security_group_ids" {
  description = "eks_cluster는 컨트롤플레인+전체 Fargate 파드 공용(Fargate는 프로파일별 SG 미지원), eks_backend는 관리형 노드그룹 전용"
  value = {
    eks_cluster        = aws_security_group.team1_sg_eks_cluster.id
    eks_backend        = aws_security_group.team1_sg_eks_backend.id
    msk                = aws_security_group.team1_sg_msk.id
    rds                = aws_security_group.team1_sg_rds.id
    redis              = aws_security_group.team1_sg_redis.id
    alb_public         = aws_security_group.team1_sg_alb_public.id
    lambda_job_trigger = aws_security_group.team1_sg_lambda_job_trigger.id
    vpc_endpoints      = aws_security_group.team1_sg_vpc_endpoints.id
  }
}

output "nat_gateway_ids" {
  value = {
    a = aws_nat_gateway.team1_nat_a.id
    b = aws_nat_gateway.team1_nat_b.id
  }
}
