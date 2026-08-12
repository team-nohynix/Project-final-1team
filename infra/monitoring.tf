# AMP + AMG + CloudWatch 알람 5종. 측정 대상과 같은 자원을 공유하면 부하가 가장 큰 순간에
# 모니터링이 먼저 죽으므로 관리형 서비스로 뺀다. 지표 수집기(Prometheus 서버, remote_write
# 설정)는 Helm으로 클러스터 안에 설치한다 — 여기서는 AMP/AMG 리소스와 그 IAM만 만든다.

resource "aws_prometheus_workspace" "team1" {
  alias = "team1-amp-truss"

  tags = {
    Team = "team1"
    Name = "team1-amp-truss"
  }
}

# AMG는 IAM Identity Center(SSO) 인증이 전제라 계정 차원 SSO 활성화가 선행돼야 한다 —
# 여기서는 워크스페이스만 만들고 사용자/그룹 매핑은 콘솔에서 별도 진행.
data "aws_iam_policy_document" "amg_assume" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["grafana.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "amg" {
  name               = "team1-amg-role"
  assume_role_policy = data.aws_iam_policy_document.amg_assume.json

  tags = {
    Team = "team1"
    Name = "team1-amg-role"
  }
}

data "aws_iam_policy_document" "amg_policy" {
  statement {
    actions = [
      "aps:ListWorkspaces",
      "aps:DescribeWorkspace",
      "aps:QueryMetrics",
      "aps:GetLabels",
      "aps:GetSeries",
      "aps:GetMetricMetadata",
    ]
    resources = ["*"]
  }
  statement {
    actions = [
      "cloudwatch:DescribeAlarmsForMetric",
      "cloudwatch:ListMetrics",
      "cloudwatch:GetMetricData",
      "cloudwatch:GetMetricStatistics",
      "logs:DescribeLogGroups",
      "logs:GetLogGroupFields",
      "logs:StartQuery",
      "logs:StopQuery",
      "logs:GetQueryResults",
    ]
    resources = ["*"]
  }
}

resource "aws_iam_role_policy" "amg" {
  name   = "team1-amg-policy"
  role   = aws_iam_role.amg.id
  policy = data.aws_iam_policy_document.amg_policy.json
}

resource "aws_grafana_workspace" "team1" {
  name                     = "team1-amg-truss"
  account_access_type      = "CURRENT_ACCOUNT"
  authentication_providers = ["AWS_SSO"]
  permission_type          = "SERVICE_MANAGED"
  role_arn                 = aws_iam_role.amg.arn
  data_sources             = ["PROMETHEUS", "CLOUDWATCH"]

  tags = {
    Team = "team1"
    Name = "team1-amg-truss"
  }
}

# --- 알람 + SNS -------------------------------------------------------------

resource "aws_sns_topic" "alerts" {
  name = "team1-sns-alerts"

  tags = {
    Team = "team1"
    Name = "team1-sns-alerts"
  }
}

# 2026-08-12 실사용 중 발견: RDS는 Aurora가 아닌 Multi-AZ DB 클러스터라 CPUUtilization이
# DBClusterIdentifier 차원으로는 전혀 발행되지 않고 인스턴스별(DBInstanceIdentifier)로만
# 나온다 — alb_5xx와 같은 클래스의 버그. cluster_members로 실제 인스턴스 ID를 순회한다.
resource "aws_cloudwatch_metric_alarm" "rds_cpu" {
  for_each = toset(aws_rds_cluster.team1_truss.cluster_members)

  alarm_name          = "team1-alarm-rds-cpu-${each.value}"
  namespace           = "AWS/RDS"
  metric_name         = "CPUUtilization"
  dimensions          = { DBInstanceIdentifier = each.value }
  statistic           = "Average"
  period              = 60
  evaluation_periods  = 5
  threshold           = 80
  comparison_operator = "GreaterThanThreshold"
  alarm_actions       = [aws_sns_topic.alerts.arn]
  ok_actions          = [aws_sns_topic.alerts.arn]

  tags = { Team = "team1", Name = "team1-alarm-rds-cpu-${each.value}" }
}

# 같은 이유: EngineCPUUtilization은 ReplicationGroupId가 아니라 노드 단위
# CacheClusterId 차원으로 발행된다. member_clusters로 실제 노드 ID를 순회한다.
resource "aws_cloudwatch_metric_alarm" "redis_cpu" {
  for_each = toset(aws_elasticache_replication_group.team1_redis.member_clusters)

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
  for_each = {
    orderapi  = data.aws_lb.orderapi.arn_suffix
    collector = data.aws_lb.collector.arn_suffix
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

output "amp_workspace_endpoint" {
  value = aws_prometheus_workspace.team1.prometheus_endpoint
}

output "amg_workspace_endpoint" {
  value = aws_grafana_workspace.team1.endpoint
}

output "sns_alerts_topic_arn" {
  value = aws_sns_topic.alerts.arn
}
