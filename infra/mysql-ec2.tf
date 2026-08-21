# RDS(Multi-AZ 클러스터, db.m6gd.large=vCPU 2개) 대신 자체 호스팅 MySQL EC2.
#
# 배경: 50배속 부하테스트에서 recorder의 쓰기량(WriteIOPS 2,000~2,800/초)이
# RDS CPU를 지속적으로 99%까지 밀어붙이는 걸 실측했다. recorder의 executions
# 쓰기 배치화(recorder/store/mysql.go, 2026-08-21)로 배치당 왕복 횟수는
# 800→3회로 줄였지만, 근본 원인인 "인스턴스 자체의 vCPU 부족"은 별개 문제로
# 남아있었다. RDS 인스턴스 클래스만 올리는 게 더 간단하고 안전하지만, 팀
# 결정으로 EC2에 직접 구축하는 쪽을 선택했다(비용 문제로 기존 RDS는 먼저
# 삭제 — rds.tf 참고).
#
# 기존 데이터는 버려도 된다고 확인받아 마이그레이션(덤프/복원) 없이 빈
# 스키마로 새로 시작한다. monitoring-ec2.tf와 완전히 같은 패턴(SSH 키 없이
# SSM만, S3 설정 버킷으로 user_data 16KB 한도 우회, AMI 고정)을 그대로
# 재사용한다.

resource "random_password" "mysql_root" {
  length  = 32
  special = true
  # MySQL 비밀번호에서 문제될 수 있는 문자(따옴표, 백슬래시, @ 등 DSN 구분자)는 제외.
  override_special = "!#%^*()-_=+"
}

locals {
  mysql_docker_compose_yml = templatefile("${path.module}/mysql-ec2/docker-compose.yml.tpl", {
    mysql_root_password = random_password.mysql_root.result
  })

  mysql_user_data = templatefile("${path.module}/mysql-ec2/user-data.sh.tpl", {
    config_bucket = aws_s3_bucket.mysql_config.bucket
  })
}

# --- 설정 파일 저장소 (S3) ----------------------------------------------------

resource "aws_s3_bucket" "mysql_config" {
  bucket = "team1-mysql-config"

  tags = {
    Team = "team1"
    Name = "team1-mysql-config"
  }
}

resource "aws_s3_bucket_public_access_block" "mysql_config" {
  bucket = aws_s3_bucket.mysql_config.id

  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_s3_bucket_server_side_encryption_configuration" "mysql_config" {
  bucket = aws_s3_bucket.mysql_config.id

  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm = "AES256"
    }
  }
}

resource "aws_s3_object" "mysql_docker_compose" {
  bucket  = aws_s3_bucket.mysql_config.id
  key     = "docker-compose.yml"
  content = local.mysql_docker_compose_yml
  etag    = md5(local.mysql_docker_compose_yml)
}

# 최초 기동 시 mysql 공식 이미지가 자동 실행 — docker-compose.yml.tpl 참고.
resource "aws_s3_object" "mysql_schema" {
  bucket  = aws_s3_bucket.mysql_config.id
  key     = "schema.sql"
  content = file("${path.module}/../recorder/schema.sql")
  etag    = filemd5("${path.module}/../recorder/schema.sql")
}

data "aws_ami" "al2023_mysql" {
  most_recent = true
  owners      = ["amazon"]

  filter {
    name   = "name"
    values = ["al2023-ami-*-x86_64"]
  }
}

# --- IAM (SSM 접속용 — SSH 키 없음) -----------------------------------------

data "aws_iam_policy_document" "mysql_ec2_assume" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["ec2.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "mysql" {
  name               = "team1-mysql-role"
  assume_role_policy = data.aws_iam_policy_document.mysql_ec2_assume.json

  tags = {
    Team = "team1"
    Name = "team1-mysql-role"
  }
}

resource "aws_iam_role_policy_attachment" "mysql_ssm" {
  role       = aws_iam_role.mysql.name
  policy_arn = "arn:aws:iam::aws:policy/AmazonSSMManagedInstanceCore"
}

# 부팅 시 user_data가 S3에서 설정 파일을 내려받는 데 필요. 읽기 전용.
data "aws_iam_policy_document" "mysql_config_read" {
  statement {
    actions   = ["s3:GetObject"]
    resources = ["${aws_s3_bucket.mysql_config.arn}/*"]
  }
}

resource "aws_iam_role_policy" "mysql_config_read" {
  name   = "team1-mysql-config-read"
  role   = aws_iam_role.mysql.id
  policy = data.aws_iam_policy_document.mysql_config_read.json
}

resource "aws_iam_instance_profile" "mysql" {
  name = "team1-mysql-profile"
  role = aws_iam_role.mysql.name

  tags = {
    Team = "team1"
    Name = "team1-mysql-profile"
  }
}

# --- 네트워크 ----------------------------------------------------------------
#
# data 서브넷(RDS가 있던 곳)이 아니라 eks_backend 서브넷에 둔다 — data 서브넷은
# 라우팅 테이블에 NAT/IGW 라우트가 전혀 없어 완전히 고립돼 있어서(recorder ->
# RDS 인바운드 3306만 필요했던 RDS와 달리) docker pull도 SSM 접속도 안 된다.
# eks_backend는 team1_private_a 라우트 테이블(NAT 게이트웨이로 인터넷 아웃바운드
# 있음)을 이미 쓰고 있어 별도 네트워크 리소스 없이 그대로 재사용 가능하다.
resource "aws_security_group" "team1_sg_mysql_ec2" {
  name        = "team1-sg-mysql-ec2"
  description = "team1 self-hosted MySQL EC2 - eks_backend only, no public access (team1_sg_rds와 동일한 규칙)"
  vpc_id      = data.terraform_remote_state.network.outputs.vpc_id

  ingress {
    description     = "recorder to MySQL write"
    from_port       = 3306
    to_port         = 3306
    protocol        = "tcp"
    security_groups = [data.terraform_remote_state.network.outputs.security_group_ids.eks_backend]
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
    Name = "team1-sg-mysql-ec2"
  }
}

# --- EC2 ---------------------------------------------------------------------

resource "aws_instance" "mysql" {
  ami                    = data.aws_ami.al2023_mysql.id
  instance_type          = "m6i.2xlarge" # 8vCPU/32GiB — RDS(db.m6gd.large, 2vCPU)의 4배
  subnet_id              = data.terraform_remote_state.network.outputs.subnet_ids.eks_backend.a
  vpc_security_group_ids = [aws_security_group.team1_sg_mysql_ec2.id]
  iam_instance_profile   = aws_iam_instance_profile.mysql.name

  user_data_base64            = base64gzip(local.mysql_user_data)
  user_data_replace_on_change = true # user_data는 최초 부팅에만 실행되므로, 바뀌면 재생성해야 실제 반영됨

  root_block_device {
    volume_size = 50
    volume_type = "gp3"
    iops        = 3000
    throughput  = 250
  }

  # monitoring-ec2.tf와 같은 이유 — most_recent=true인 AMI가 새로 나올 때마다
  # plan/apply가 살아있는 인스턴스를 강제로 replace하려 드는 것을 막는다.
  lifecycle {
    ignore_changes = [ami]
  }

  tags = {
    Team = "team1"
    Name = "team1-mysql"
  }
}

# monitoring.tf에서 지운 team1-alarm-rds-cpu와 같은 자리를 대신한다 — EC2는
# 인스턴스 ID 하나가 곧 CPU 지표 차원이라 RDS 클러스터 때처럼 for_each로 여러
# 멤버를 순회할 필요가 없다.
resource "aws_cloudwatch_metric_alarm" "mysql_ec2_cpu" {
  alarm_name          = "team1-alarm-mysql-ec2-cpu"
  namespace           = "AWS/EC2"
  metric_name         = "CPUUtilization"
  dimensions          = { InstanceId = aws_instance.mysql.id }
  statistic           = "Average"
  period              = 60
  evaluation_periods  = 5
  threshold           = 80
  comparison_operator = "GreaterThanThreshold"
  alarm_actions       = [aws_sns_topic.alerts.arn]
  ok_actions          = [aws_sns_topic.alerts.arn]

  tags = { Team = "team1", Name = "team1-alarm-mysql-ec2-cpu" }
}

output "mysql_private_ip" {
  value = aws_instance.mysql.private_ip
}

output "mysql_root_password" {
  value     = random_password.mysql_root.result
  sensitive = true
}
