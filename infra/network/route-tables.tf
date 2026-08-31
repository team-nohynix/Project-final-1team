# 컴퓨트는 같은 AZ의 NAT만 사용(교차 AZ 라우팅 없음). 데이터 계층은 NAT 없음.

resource "aws_route_table" "team1_public_rt" {
  vpc_id = aws_vpc.team1_vpc.id

  route {
    cidr_block = "0.0.0.0/0"
    gateway_id = aws_internet_gateway.team1_igw.id
  }

  tags = {
    Team = "team1"
    Name = "team1-public-rt"
  }
}

resource "aws_route_table_association" "team1_public_a" {
  subnet_id      = aws_subnet.team1_public_a.id
  route_table_id = aws_route_table.team1_public_rt.id
}

resource "aws_route_table_association" "team1_public_b" {
  subnet_id      = aws_subnet.team1_public_b.id
  route_table_id = aws_route_table.team1_public_rt.id
}

# AZ-a 컴퓨트 (백엔드 노드그룹/Karpenter 노드 + 수집기/AI트레이더/리플레이 Fargate 공유)

resource "aws_route_table" "team1_private_a" {
  vpc_id = aws_vpc.team1_vpc.id

  route {
    cidr_block     = "0.0.0.0/0"
    nat_gateway_id = aws_nat_gateway.team1_nat_a.id
  }

  tags = {
    Team = "team1"
    Name = "team1-private-rt-a"
  }
}

resource "aws_route_table_association" "team1_eks_backend_a" {
  subnet_id      = aws_subnet.team1_eks_backend_a.id
  route_table_id = aws_route_table.team1_private_a.id
}

resource "aws_route_table_association" "team1_eks_collector_a" {
  subnet_id      = aws_subnet.team1_eks_collector_a.id
  route_table_id = aws_route_table.team1_private_a.id
}

resource "aws_route_table_association" "team1_eks_aitrader_a" {
  subnet_id      = aws_subnet.team1_eks_aitrader_a.id
  route_table_id = aws_route_table.team1_private_a.id
}

resource "aws_route_table_association" "team1_eks_replay_a" {
  subnet_id      = aws_subnet.team1_eks_replay_a.id
  route_table_id = aws_route_table.team1_private_a.id
}

# AZ-b 컴퓨트 (백엔드 노드그룹/Karpenter 노드 + 수집기/AI트레이더/리플레이 Fargate 공유)

resource "aws_route_table" "team1_private_b" {
  vpc_id = aws_vpc.team1_vpc.id

  route {
    cidr_block     = "0.0.0.0/0"
    nat_gateway_id = aws_nat_gateway.team1_nat_b.id
  }

  tags = {
    Team = "team1"
    Name = "team1-private-rt-b"
  }
}

resource "aws_route_table_association" "team1_eks_backend_b" {
  subnet_id      = aws_subnet.team1_eks_backend_b.id
  route_table_id = aws_route_table.team1_private_b.id
}

resource "aws_route_table_association" "team1_eks_collector_b" {
  subnet_id      = aws_subnet.team1_eks_collector_b.id
  route_table_id = aws_route_table.team1_private_b.id
}

resource "aws_route_table_association" "team1_eks_aitrader_b" {
  subnet_id      = aws_subnet.team1_eks_aitrader_b.id
  route_table_id = aws_route_table.team1_private_b.id
}

resource "aws_route_table_association" "team1_eks_replay_b" {
  subnet_id      = aws_subnet.team1_eks_replay_b.id
  route_table_id = aws_route_table.team1_private_b.id
}

# 데이터 — a/b (S3 Gateway Endpoint는 endpoints.tf)

resource "aws_route_table" "team1_data_a" {
  vpc_id = aws_vpc.team1_vpc.id

  tags = {
    Team = "team1"
    Name = "team1-data-rt-a"
  }
}

resource "aws_route_table_association" "team1_data_a" {
  subnet_id      = aws_subnet.team1_data_a.id
  route_table_id = aws_route_table.team1_data_a.id
}

resource "aws_route_table" "team1_data_b" {
  vpc_id = aws_vpc.team1_vpc.id

  tags = {
    Team = "team1"
    Name = "team1-data-rt-b"
  }
}

resource "aws_route_table_association" "team1_data_b" {
  subnet_id      = aws_subnet.team1_data_b.id
  route_table_id = aws_route_table.team1_data_b.id
}
