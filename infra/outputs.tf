# 카펜터/ALB Controller/GitHub Actions/모니터링 관련 output은 각자 파일(karpenter.tf,
# alb-controller.tf, cicd.tf, edge.tf, monitoring.tf)에 있다. 여기는 나머지 — Helm
# values나 애플리케이션 배포 파이프라인이 참조할 값들.

output "eks_cluster_name" {
  value = aws_eks_cluster.team1.name
}

output "eks_cluster_endpoint" {
  value = aws_eks_cluster.team1.endpoint
}

output "eks_cluster_certificate_authority_data" {
  value = aws_eks_cluster.team1.certificate_authority[0].data
}

output "eks_oidc_provider_arn" {
  value = aws_iam_openid_connect_provider.team1_eks.arn
}

output "ecr_repository_url" {
  value = aws_ecr_repository.team1.repository_url
}

output "s3_bucket_names" {
  value = {
    market_data   = aws_s3_bucket.market_data.id
    order_records = aws_s3_bucket.order_records.id
    trade_results = aws_s3_bucket.trade_results.id
    frontend      = aws_s3_bucket.frontend.id
  }
}

output "rds_writer_endpoint" {
  value = aws_rds_cluster.team1_truss.endpoint
}

output "rds_reader_endpoint" {
  value = aws_rds_cluster.team1_truss.reader_endpoint
}

output "rds_master_user_secret_arn" {
  value = aws_rds_cluster.team1_truss.master_user_secret[0].secret_arn
}

output "redis_primary_endpoint" {
  value = aws_elasticache_replication_group.team1_redis.primary_endpoint_address
}

output "redis_reader_endpoint" {
  value = aws_elasticache_replication_group.team1_redis.reader_endpoint_address
}

output "msk_bootstrap_brokers_sasl_iam" {
  value = aws_msk_serverless_cluster.team1_truss.bootstrap_brokers_sasl_iam
}

output "job_trigger_queue_url" {
  value = aws_sqs_queue.job_trigger.id
}

output "irsa_role_arns" {
  description = "k8s/*.yaml ServiceAccount의 eks.amazonaws.com/role-arn 어노테이션에 넣을 값"
  value = {
    ingest_api      = aws_iam_role.sa_ingest_api.arn
    matching_engine = aws_iam_role.sa_matching_engine.arn
    recorder        = aws_iam_role.sa_recorder.arn
    collector       = aws_iam_role.sa_collector.arn
    ai_trader       = aws_iam_role.sa_ai_trader.arn
    replay_engine   = aws_iam_role.sa_replay_engine.arn
    alb_controller  = aws_iam_role.alb_controller.arn
  }
}
