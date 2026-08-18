# AZ당 NAT 1개(a/b). 실질 용도는 시세수집기→업비트 아웃바운드뿐(S3/ECR/CloudWatch/STS/
# Bedrock/SQS는 VPC 엔드포인트로 처리, endpoints.tf).

resource "aws_eip" "team1_nat_a" {
  domain = "vpc"

  tags = {
    Team = "team1"
    Name = "team1-nat-eip-a"
  }

  depends_on = [aws_internet_gateway.team1_igw]
}

resource "aws_eip" "team1_nat_b" {
  domain = "vpc"

  tags = {
    Team = "team1"
    Name = "team1-nat-eip-b"
  }

  depends_on = [aws_internet_gateway.team1_igw]
}

resource "aws_nat_gateway" "team1_nat_a" {
  allocation_id = aws_eip.team1_nat_a.id
  subnet_id     = aws_subnet.team1_public_a.id

  tags = {
    Team = "team1"
    Name = "team1-nat-a"
  }

  depends_on = [aws_internet_gateway.team1_igw]
}

resource "aws_nat_gateway" "team1_nat_b" {
  allocation_id = aws_eip.team1_nat_b.id
  subnet_id     = aws_subnet.team1_public_b.id

  tags = {
    Team = "team1"
    Name = "team1-nat-b"
  }

  depends_on = [aws_internet_gateway.team1_igw]
}
