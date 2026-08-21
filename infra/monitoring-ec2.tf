# 격리 모니터링 EC2 — 자체 호스팅 Prometheus+Grafana(Docker Compose). AMP/AMG는 안 쓴다
# (AMG는 이 계정이 속한 조직의 IAM Identity Center SSO 권한이 없어 구조적으로 막힘,
# infra/k8s/monitoring/prometheus-values.yaml 주석 참고).
#
# 이전에 이 자리에 수동으로 만든 EC2(team1-monitoring)가 있었는데 Terraform 관리 밖이라
# SSH 키도 IAM 프로파일도 없어 아무도 못 고치는 상태였고, prometheus.yml에 미치환
# ${orderapi_endpoint} 같은 셸 변수가 그대로 박혀 있었다 — 이번엔 재현 가능하게 새로 만든다.
#
# 네트워크 격리: 이 EC2는 EKS 노드/파드 보안그룹과 아무 신뢰관계도 맺지 않는다. 스크레이프는
# EKS API 서버 프록시 경로(/api/v1/.../proxy/metrics)로만 하고, 접근 통제는 오직 IAM
# Access Entry + RBAC(k8s/monitoring/external-prometheus-rbac.yaml)로만 한다.

variable "monitoring_grafana_admin_password" {
  description = "모니터링 EC2 Grafana admin 비밀번호 — terraform.tfvars(gitignore 대상)에서 설정"
  type        = string
  sensitive   = true
}

locals {
  monitoring_prometheus_yml = templatefile("${path.module}/monitoring-ec2/prometheus.yml.tpl", {
    eks_api_host = replace(aws_eks_cluster.team1.endpoint, "https://", "")
  })

  monitoring_docker_compose_yml = templatefile("${path.module}/monitoring-ec2/docker-compose.yml.tpl", {
    grafana_admin_password = var.monitoring_grafana_admin_password
  })

  # user_data는 여기 나열된 파일 내용을 전부 그대로 박아 넣었었는데, 대시보드
  # JSON이 커지면서(2026-08-20, 시세 수집기/AI 트레이더 패널 추가 후 61개 패널)
  # gzip+base64를 거치고도 EC2의 user_data 16,384바이트 한도를 넘어 인스턴스
  # 생성 자체가 실패했다(RunInstances: "User data is limited to 16384 bytes") —
  # 대시보드를 조금만 고쳐도 인스턴스가 통째로 재생성되던 문제와 같은 근본
  # 원인. 이제 이 파일들은 S3(아래 aws_s3_object)에 올려두고, user_data는
  # 부팅 시 S3에서 내려받기만 한다 — user_data 자체는 몇 줄 안 되니 앞으로
  # 다시 이 한도에 걸릴 일이 없고, 내용이 바뀌어도(S3 객체만 갱신) EC2가
  # 재생성되지 않는다.
  monitoring_user_data = templatefile("${path.module}/monitoring-ec2/user-data.sh.tpl", {
    aws_region       = "ap-northeast-2"
    eks_cluster_name = aws_eks_cluster.team1.name
    config_bucket    = aws_s3_bucket.monitoring_config.bucket
  })
}

# --- 모니터링 설정 파일 저장소 (S3) ------------------------------------------
# EC2 user_data에 직접 박아넣던 걸 여기로 옮겼다 — 이유는 위 monitoring_user_data
# 주석 참고. 객체 내용이 바뀌면(etag=md5) S3 객체만 갱신되고 EC2는 안 건드린다.

resource "aws_s3_bucket" "monitoring_config" {
  bucket = "team1-monitoring-config"

  tags = {
    Team = "team1"
    Name = "team1-monitoring-config"
  }
}

resource "aws_s3_bucket_public_access_block" "monitoring_config" {
  bucket = aws_s3_bucket.monitoring_config.id

  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_s3_bucket_server_side_encryption_configuration" "monitoring_config" {
  bucket = aws_s3_bucket.monitoring_config.id

  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm = "AES256"
    }
  }
}

resource "aws_s3_object" "monitoring_prometheus_yml" {
  bucket  = aws_s3_bucket.monitoring_config.id
  key     = "prometheus.yml"
  content = local.monitoring_prometheus_yml
  etag    = md5(local.monitoring_prometheus_yml)
}

resource "aws_s3_object" "monitoring_grafana_datasource" {
  bucket  = aws_s3_bucket.monitoring_config.id
  key     = "datasource.yml"
  content = file("${path.module}/monitoring-ec2/grafana-datasource.yml")
  etag    = filemd5("${path.module}/monitoring-ec2/grafana-datasource.yml")
}

resource "aws_s3_object" "monitoring_grafana_provider" {
  bucket  = aws_s3_bucket.monitoring_config.id
  key     = "provider.yml"
  content = file("${path.module}/monitoring-ec2/grafana-dashboard-provider.yml")
  etag    = filemd5("${path.module}/monitoring-ec2/grafana-dashboard-provider.yml")
}

resource "aws_s3_object" "monitoring_dashboard_team1" {
  bucket  = aws_s3_bucket.monitoring_config.id
  key     = "team1-overview.json"
  content = file("${path.module}/monitoring-ec2/dashboards/team1-overview.json")
  etag    = filemd5("${path.module}/monitoring-ec2/dashboards/team1-overview.json")
}

# 프론트 "시스템 종합 현황" 화면과 똑같이 생긴 별도 대시보드(2026-08-19) — 대시보드
# provider가 폴더 전체를 보므로 객체만 늘리면 됨(grafana-dashboard-provider.yml 참고).
resource "aws_s3_object" "monitoring_dashboard_system" {
  bucket  = aws_s3_bucket.monitoring_config.id
  key     = "system-overview.json"
  content = file("${path.module}/monitoring-ec2/dashboards/system-overview.json")
  etag    = filemd5("${path.module}/monitoring-ec2/dashboards/system-overview.json")
}

resource "aws_s3_object" "monitoring_docker_compose" {
  bucket  = aws_s3_bucket.monitoring_config.id
  key     = "docker-compose.yml"
  content = local.monitoring_docker_compose_yml
  etag    = md5(local.monitoring_docker_compose_yml)
}

data "aws_ami" "al2023" {
  most_recent = true
  owners      = ["amazon"]

  filter {
    name   = "name"
    values = ["al2023-ami-*-x86_64"]
  }
}

# --- IAM (SSM 접속용 — SSH 키 없음) -----------------------------------------

data "aws_iam_policy_document" "monitoring_ec2_assume" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["ec2.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "monitoring" {
  name               = "team1-monitoring-role"
  assume_role_policy = data.aws_iam_policy_document.monitoring_ec2_assume.json

  tags = {
    Team = "team1"
    Name = "team1-monitoring-role"
  }
}

resource "aws_iam_role_policy_attachment" "monitoring_ssm" {
  role       = aws_iam_role.monitoring.name
  policy_arn = "arn:aws:iam::aws:policy/AmazonSSMManagedInstanceCore"
}

# eks:DescribeCluster는 update-kubeconfig/토큰 발급에 필요 — job-trigger.tf의
# lambda_job_trigger 정책과 같은 이유.
data "aws_iam_policy_document" "monitoring_eks_describe" {
  statement {
    actions   = ["eks:DescribeCluster"]
    resources = [aws_eks_cluster.team1.arn]
  }
}

resource "aws_iam_role_policy" "monitoring_eks_describe" {
  name   = "team1-monitoring-eks-describe"
  role   = aws_iam_role.monitoring.id
  policy = data.aws_iam_policy_document.monitoring_eks_describe.json
}

# Grafana의 CloudWatch 데이터소스(grafana-datasource.yml)용 — Kafka(MSK)/RDS/Redis는
# Prometheus exporter가 없고 CloudWatch 지표만 있어서, monitoring.tf의 알람이 보는
# 것과 같은 지표를 Grafana에서도 직접 조회하려면 필요하다(2026-08-19). 읽기 전용.
data "aws_iam_policy_document" "monitoring_cloudwatch_read" {
  statement {
    actions = [
      "cloudwatch:GetMetricData",
      "cloudwatch:GetMetricStatistics",
      "cloudwatch:ListMetrics",
      "cloudwatch:DescribeAlarms",
      "tag:GetResources",
    ]
    resources = ["*"]
  }
}

resource "aws_iam_role_policy" "monitoring_cloudwatch_read" {
  name   = "team1-monitoring-cloudwatch-read"
  role   = aws_iam_role.monitoring.id
  policy = data.aws_iam_policy_document.monitoring_cloudwatch_read.json
}

# 부팅 시 user_data가 S3에서 설정 파일을 내려받는 데 필요(위 monitoring_config
# 버킷 참고). 읽기 전용.
data "aws_iam_policy_document" "monitoring_config_read" {
  statement {
    actions   = ["s3:GetObject"]
    resources = ["${aws_s3_bucket.monitoring_config.arn}/*"]
  }
}

resource "aws_iam_role_policy" "monitoring_config_read" {
  name   = "team1-monitoring-config-read"
  role   = aws_iam_role.monitoring.id
  policy = data.aws_iam_policy_document.monitoring_config_read.json
}

resource "aws_iam_instance_profile" "monitoring" {
  name = "team1-monitoring-profile"
  role = aws_iam_role.monitoring.name

  tags = {
    Team = "team1"
    Name = "team1-monitoring-profile"
  }
}

# AWS 관리형 액세스 정책은 붙이지 않고 kubernetes_groups로만 매핑한다 — 실제 RBAC은
# k8s/monitoring/external-prometheus-rbac.yaml의 ClusterRole/ClusterRoleBinding에서 적용
# (job-trigger.tf의 aws_eks_access_entry.lambda_job_trigger와 같은 패턴).
resource "aws_eks_access_entry" "monitoring" {
  cluster_name      = aws_eks_cluster.team1.name
  principal_arn     = aws_iam_role.monitoring.arn
  type              = "STANDARD"
  kubernetes_groups = ["team1-monitoring-viewers"]
}

# --- 네트워크 ----------------------------------------------------------------

resource "aws_security_group" "team1_sg_monitoring" {
  name        = "team1-sg-monitoring"
  description = "team1 monitoring EC2 (Prometheus+Grafana) - browser access + EKS API server only, no trust with EKS node/pod SGs"
  vpc_id      = data.terraform_remote_state.network.outputs.vpc_id

  ingress {
    description = "Grafana"
    from_port   = 3000
    to_port     = 3000
    protocol    = "tcp"
    cidr_blocks = ["0.0.0.0/0"]
  }

  ingress {
    description = "Grafana on :80 (monitor.jhyang.click)"
    from_port   = 80
    to_port     = 80
    protocol    = "tcp"
    cidr_blocks = ["0.0.0.0/0"]
  }

  ingress {
    description = "Prometheus"
    from_port   = 9090
    to_port     = 9090
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
    Name = "team1-sg-monitoring"
  }
}

# 클러스터가 private+public 엔드포인트를 둘 다 켜놨는데(eks-cluster.tf), 같은 VPC 안에서는
# VPC DNS가 클러스터 API 호스트네임을 항상 private ENI IP로 풀어준다 — 그래서 퍼블릭
# 서브넷에 있어도 이 SG↔eks_cluster SG 규칙이 없으면 API 서버로 가는 패킷이 그냥 죽는다
# (job-trigger.tf의 team1_cluster_from_lambda와 같은 이유/패턴). 노드/파드 SG는 안 건드림 —
# API 서버(컨트롤플레인)만 대상이라 "네트워크 격리" 설계 취지는 그대로 유지된다.
resource "aws_security_group_rule" "team1_cluster_from_monitoring" {
  type                     = "ingress"
  security_group_id        = data.terraform_remote_state.network.outputs.security_group_ids.eks_cluster
  source_security_group_id = aws_security_group.team1_sg_monitoring.id
  from_port                = 443
  to_port                  = 443
  protocol                 = "tcp"
  description              = "Monitoring EC2 to EKS private endpoint (API server proxy scrape)"
}

resource "aws_eip" "monitoring" {
  domain = "vpc"

  tags = {
    Team = "team1"
    Name = "team1-eip-monitoring"
  }
}

# --- EC2 ---------------------------------------------------------------------

# 리소스 이름 monitoring_v2: 예전에 수동으로 만들었던 미관리 EC2가 aws_instance.monitoring
# 이름으로 이미 state에 남아 있어서(관리 밖에서 config 없이 방치된 orphan), 그 주소를 그대로
# 쓰면 이 config가 그 기존 인스턴스를 "업데이트"하려 든다 — 실수로 그 인스턴스를 건드리지
# 않도록 새 주소를 쓴다. 기존 orphan(aws_instance.monitoring, aws_security_group.monitoring,
# data.aws_ami.ubuntu)은 새 인스턴스 정상 동작 확인 후 별도로 정리한다.
resource "aws_instance" "monitoring_v2" {
  ami = data.aws_ami.al2023.id
  # 2026-08-21: t3.small(2GB RAM)이 Prometheus+Grafana 두 컨테이너를 감당 못 해
  # 주기적으로 완전히 멎는(SSM도 응답 없음, docker ps조차 안 됨) 문제가 반복돼
  # t3.medium(4GB)으로 올림 — pod별 스크레이프로 고치면서(서비스당 타겟 1개 ->
  # 레플리카 수만큼) 타겟/시계열 수가 늘어난 것과 시기가 겹친다. CPU 크레딧은
  # 사고 당시에도 넉넉했어서(t3.small 최대치 근처) CPU가 아니라 메모리 쪽
  # 압박으로 추정.
  instance_type          = "t3.medium"
  subnet_id              = data.terraform_remote_state.network.outputs.subnet_ids.public.a
  vpc_security_group_ids = [aws_security_group.team1_sg_monitoring.id]
  iam_instance_profile   = aws_iam_instance_profile.monitoring.name
  # gzip 압축 — 대시보드 JSON이 커지면서 평문 user_data가 EC2의 16KB 한도를 넘겨서
  # (2026-08-13) 압축으로 전환. EC2가 gzip 매직바이트를 자동 인식해서 부팅 시 그대로 풀어 실행한다.
  user_data_base64            = base64gzip(local.monitoring_user_data)
  user_data_replace_on_change = true # user_data는 최초 부팅에만 실행되므로, 바뀌면 재생성해야 실제 반영됨

  root_block_device {
    volume_size = 30 # AL2023 AMI 스냅샷 최소 요구치
    volume_type = "gp3"
  }

  # data.aws_ami.al2023가 most_recent=true라, AWS가 새 AL2023 AMI를 낼 때마다
  # ami 값이 바뀌어서 plan/apply가 이 살아있는 인스턴스를 강제로 replace하려 든다
  # (2026-08-19, CI apply 승인 직전에 발견) — user_data로 재현 가능하니 AMI는
  # 최초 생성 시점 값으로 고정하고, 나중에 의도적으로 새 AMI를 쓰고 싶으면
  # 이 lifecycle 블록을 지우고 명시적으로 재생성한다.
  lifecycle {
    ignore_changes = [ami]
  }

  tags = {
    Team = "team1"
    Name = "team1-monitoring"
  }
}

resource "aws_eip_association" "monitoring" {
  instance_id   = aws_instance.monitoring_v2.id
  allocation_id = aws_eip.monitoring.id
}

output "monitoring_public_ip" {
  value = aws_eip.monitoring.public_ip
}

# monitor.jhyang.click 클릭하면 바로 Grafana(포트 80, docker-compose.yml.tpl에서
# 3000과 같이 80도 매핑)가 뜨도록 — ALB가 아니라 EIP라 alias가 아닌 값 레코드.
resource "aws_route53_record" "monitoring" {
  zone_id = data.aws_route53_zone.team1.zone_id
  name    = "monitor.jhyang.click"
  type    = "A"
  ttl     = 300
  records = [aws_eip.monitoring.public_ip]
}
