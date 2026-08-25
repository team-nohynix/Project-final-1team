# CloudWatch 알람 5종 + SNS. AMP/AMG는 안 쓴다 — AMG는 이 계정이 속한 조직의 IAM Identity
# Center SSO 권한이 없어 구조적으로 막혀서(infra/k8s/monitoring/prometheus-values.yaml 주석
# 참고), 격리된 모니터링은 infra/monitoring-ec2.tf의 자체 호스팅 EC2로 대체했다
# (2026-08-13, 한 번도 apply된 적 없던 AMP workspace/AMG workspace/관련 IAM 삭제).

# --- 알람 + SNS -------------------------------------------------------------

resource "aws_sns_topic" "alerts" {
  name = "team1-sns-alerts"

  tags = {
    Team = "team1"
    Name = "team1-sns-alerts"
  }
}

# 같은 이유: EngineCPUUtilization은 ReplicationGroupId가 아니라 노드 단위
# CacheClusterId 차원으로 발행된다. 원래 member_clusters(실제 생성된 노드 ID
# 목록)로 순회했었는데, 이 값은 replication group을 "이번 apply 안에서" 새로
# 만드는 경우 apply 전에는 알 수 없어("known after apply") for_each가 그
# 자체로 막혀버린다(EKS 전체 destroy→apply 리허설 중 실제로 겪음, 2026-08-25).
# ElastiCache의 노드 ID는 "<replication_group_id>-001", "-002", ... 형식으로
# 결정적이고, replication_group_id/num_cache_clusters는 리소스가 생기기 전에도
# 이미 아는 설정값이라 plan 시점에 계산 가능하다.
resource "aws_cloudwatch_metric_alarm" "redis_cpu" {
  for_each = toset([
    for i in range(1, aws_elasticache_replication_group.team1_redis.num_cache_clusters + 1) :
    "${aws_elasticache_replication_group.team1_redis.replication_group_id}-${format("%03d", i)}"
  ])

  alarm_name          = "team1-alarm-redis-cpu-${each.value}"
  namespace           = "AWS/ElastiCache"
  metric_name         = "EngineCPUUtilization"
  dimensions          = { CacheClusterId = each.value }
  statistic           = "Average"
  period              = 60
  evaluation_periods  = 5
  threshold           = 80
  comparison_operator = "GreaterThanThreshold"
  alarm_actions       = [aws_sns_topic.alerts.arn]
  ok_actions          = [aws_sns_topic.alerts.arn]

  tags = { Team = "team1", Name = "team1-alarm-redis-cpu-${each.value}" }
}

# MSK Serverless는 브로커 단위 지표가 없어 클러스터 단위 컨슈머 그룹 랙으로 건강도를 본다.
resource "aws_cloudwatch_metric_alarm" "msk_health" {
  alarm_name          = "team1-alarm-msk-health"
  namespace           = "AWS/Kafka"
  metric_name         = "SumOffsetLag"
  dimensions          = { "Cluster Name" = aws_msk_serverless_cluster.team1_truss.cluster_name }
  statistic           = "Maximum"
  period              = 60
  evaluation_periods  = 5
  threshold           = 100000
  comparison_operator = "GreaterThanThreshold"
  alarm_actions       = [aws_sns_topic.alerts.arn]
  ok_actions          = [aws_sns_topic.alerts.arn]
  treat_missing_data  = "notBreaching"

  tags = { Team = "team1", Name = "team1-alarm-msk-health" }
}

# 2026-08-11 작성 당시엔 ALB가 Ingress로 배포 후에야 생기는 리소스라 차원(dimension)
# 없이 계정 전체를 잡아뒀었는데, dimension이 아예 없으면 ALB 지표(항상 LoadBalancer
# 차원으로 나옴)에는 매칭되는 데이터포인트가 없어서 이 알람이 사실상 한 번도 안
# 울리는 상태였다 — 2026-08-12에 실제 ALB 2개(접수 API, 시세수집기, edge.tf의
# data.aws_lb.orderapi/collector)가 생긴 뒤 각각 차원을 좁혀 진짜 동작하게 정정.
resource "aws_cloudwatch_metric_alarm" "alb_5xx" {
  # edge.tf의 data.external.orderapi_alb/collector_alb 주석 참고 — ALB가 아직
  # 없으면(EKS 전체 destroy→apply 직후) 각각 빈 문자열로 빠지고, 이 for_each는
  # 그 항목을 통째로 건너뛴다(에러 없이 알람 0~2개) — ALB가 생긴 뒤 다음
  # apply에서 자연히 채워진다.
  for_each = {
    for k, v in {
      orderapi  = data.external.orderapi_alb.result.arn_suffix
      collector = data.external.collector_alb.result.arn_suffix
    } : k => v if v != ""
  }

  alarm_name          = "team1-alarm-alb-5xx-${each.key}"
  namespace           = "AWS/ApplicationELB"
  metric_name         = "HTTPCode_Target_5XX_Count"
  statistic           = "Sum"
  period              = 60
  evaluation_periods  = 5
  threshold           = 50
  comparison_operator = "GreaterThanThreshold"
  dimensions          = { LoadBalancer = each.value }
  alarm_actions       = [aws_sns_topic.alerts.arn]
  ok_actions          = [aws_sns_topic.alerts.arn]
  treat_missing_data  = "notBreaching"

  tags = { Team = "team1", Name = "team1-alarm-alb-5xx-${each.key}" }
}

resource "aws_cloudwatch_metric_alarm" "nodegroup_health" {
  alarm_name          = "team1-alarm-nodegroup-health"
  namespace           = "ContainerInsights"
  metric_name         = "cluster_failed_node_count"
  dimensions          = { ClusterName = aws_eks_cluster.team1.name }
  statistic           = "Maximum"
  period              = 60
  evaluation_periods  = 5
  threshold           = 0
  comparison_operator = "GreaterThanThreshold"
  alarm_actions       = [aws_sns_topic.alerts.arn]
  ok_actions          = [aws_sns_topic.alerts.arn]
  treat_missing_data  = "notBreaching"

  tags = { Team = "team1", Name = "team1-alarm-nodegroup-health" }
}

resource "aws_cloudwatch_log_group" "eks_cluster" {
  name              = "/aws/eks/team1-eks/cluster"
  retention_in_days = 30 # CloudWatch 허용값 중 15일 초과 최소값

  tags = {
    Team = "team1"
    Name = "team1-eks-cluster-logs"
  }
}

output "sns_alerts_topic_arn" {
  value = aws_sns_topic.alerts.arn
}
