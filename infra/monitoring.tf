# CloudWatch 알람 5종 + SNS. AMP/AMG는 안 쓴다 — AMG는 이 계정이 속한 조직의 IAM Identity
# Center SSO 권한이 없어 구조적으로 막혀서, 모니터링은 infra/monitoring-ec2.tf의 자체
# 호스팅 EC2로 대체했다.

# --- 알람 + SNS -------------------------------------------------------------

resource "aws_sns_topic" "alerts" {
  name = "team1-sns-alerts"

  tags = {
    Team = "team1"
    Name = "team1-sns-alerts"
  }
}

# EngineCPUUtilization은 ReplicationGroupId가 아니라 노드 단위 CacheClusterId 차원으로
# 발행된다. member_clusters(실제 생성된 노드 ID 목록)로 순회하면 replication group을
# 새로 만드는 apply에서는 그 값을 apply 전에 알 수 없어("known after apply") for_each가
# 막힌다 — ElastiCache의 노드 ID는 "<replication_group_id>-001", "-002", ... 형식으로
# 결정적이고, replication_group_id/num_cache_clusters는 리소스가 생기기 전에도 이미
# 아는 설정값이라 plan 시점에 계산 가능하다.
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

# ALB 지표(항상 LoadBalancer 차원으로 나옴)에 매칭되려면 각 ALB의 ARN suffix로 차원을
# 좁혀야 한다 — dimension 없이 계정 전체를 잡으면 매칭되는 데이터포인트가 없어 알람이
# 사실상 울리지 않는다.
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
