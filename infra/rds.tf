# RDS Multi-AZ DB 클러스터(라이터 1 + 리더 2, Aurora 아님). db_cluster_instance_class만
# 지정하면 3노드 토폴로지가 결정되므로 Aurora와 달리 aws_rds_cluster_instance는 따로
# 만들지 않는다. db.m6gd.large는 이 계정/리전에서 2a/2b/2d만 지원한다(2c 미지원).

resource "aws_db_subnet_group" "team1_rds" {
  name = "team1-rds-subnet-group"
  subnet_ids = [
    data.terraform_remote_state.network.outputs.subnet_ids.data.a,
    data.terraform_remote_state.network.outputs.subnet_ids.data.b,
    data.terraform_remote_state.network.outputs.subnet_ids.data.d,
  ]

  tags = {
    Team = "team1"
    Name = "team1-rds-subnet-group"
  }
}

resource "aws_rds_cluster" "team1_truss" {
  cluster_identifier = "team1-truss-db"

  engine         = "mysql"
  engine_version = "8.4.10"

  db_cluster_instance_class = "db.m6gd.large"
  storage_type              = "gp3"
  allocated_storage         = 100
  # AWS가 자동 할당한 실제 값(aws rds describe-db-clusters로 확인).
  iops              = 3000
  storage_encrypted = true

  availability_zones = ["ap-northeast-2a", "ap-northeast-2b", "ap-northeast-2d"]

  db_subnet_group_name   = aws_db_subnet_group.team1_rds.name
  vpc_security_group_ids = [data.terraform_remote_state.network.outputs.security_group_ids.rds]

  master_username             = "team1admin"
  manage_master_user_password = true

  # Multi-AZ DB 클러스터는 백업을 완전히 끌 수 없다 — 0을 넣어도 AWS가 최솟값 1로 강제한다
  # (실제 apply로 확인됨). 그 실제 값을 명시해 드리프트를 없앤다.
  backup_retention_period = 1
  deletion_protection     = false
  skip_final_snapshot     = true

  tags = {
    Team = "team1"
    Name = "team1-truss-db"
  }
}
