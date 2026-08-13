# Prometheus + Grafana 모니터링 스택 (EC2)

resource "aws_security_group" "monitoring" {
  name        = "team1-monitoring-sg"
  description = "Prometheus + Grafana"
  vpc_id      = aws_eks_cluster.team1.vpc_config[0].vpc_id

  ingress {
    from_port   = 9090
    to_port     = 9090
    protocol    = "tcp"
    cidr_blocks = ["0.0.0.0/0"]
  }

  ingress {
    from_port   = 3000
    to_port     = 3000
    protocol    = "tcp"
    cidr_blocks = ["0.0.0.0/0"]
  }

  ingress {
    from_port   = 22
    to_port     = 22
    protocol    = "tcp"
    cidr_blocks = ["0.0.0.0/0"]
  }

  egress {
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }

  tags = {
    Name = "team1-monitoring-sg"
    Team = "team1"
  }
}

data "aws_subnets" "eks" {
  filter {
    name   = "vpc-id"
    values = [aws_eks_cluster.team1.vpc_config[0].vpc_id]
  }
}

data "aws_ami" "ubuntu" {
  most_recent = true
  owners      = ["099720109477"]
  filter {
    name   = "name"
    values = ["ubuntu/images/hvm-ssd/ubuntu-jammy-22.04-amd64-server-*"]
  }
}

resource "aws_instance" "monitoring" {
  ami                         = data.aws_ami.ubuntu.id
  instance_type               = "t3.medium"
  subnet_id                   = data.aws_subnets.eks.ids[0]
  vpc_security_group_ids      = [aws_security_group.monitoring.id]
  associate_public_ip_address = true

  user_data = base64encode(file("${path.module}/monitoring-init.sh"))

  tags = {
    Name = "team1-monitoring"
    Team = "team1"
  }
}

output "monitoring_prometheus_url" {
  value = "http://${aws_instance.monitoring.public_ip}:9090"
}

output "monitoring_grafana_url" {
  value = "http://${aws_instance.monitoring.public_ip}:3000"
}
