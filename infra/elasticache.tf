# ElastiCache Redis — 프라이머리(data-a)+레플리카(data-b), 호가창 스냅샷 + 세션 배타 잠금
# (orderapi 세션가드) 겸용. cache.t4g.medium(버스터블) — 리플레이 고배속 구간 CPU 크레딧
# 소진 여부는 부하 시험에서 확인.

# AUTH 토큰 — orderapi/matching/recorder 세 모듈 다 REDIS_PASSWORD를 선택 환경변수로
# 읽으므로(비우면 인증 없이 붙는 정상 경로) SG 격리에 애플리케이션 계층 인증을 더한다.
# mysql_root와 같은 이유로 DSN에 문제될 수 있는 특수문자(따옴표, 백슬래시, @)는 제외 —
# ElastiCache auth_token은 추가로 "/"·공백도 금지라 override_special에서 같이 뺐다.
resource "random_password" "redis_auth" {
  length           = 32
  special          = true
  override_special = "!#%^*()-_=+"
}

resource "aws_elasticache_subnet_group" "team1_redis" {
  name = "team1-redis-subnet-group"
  subnet_ids = [
    data.terraform_remote_state.network.outputs.subnet_ids.data.a,
    data.terraform_remote_state.network.outputs.subnet_ids.data.b,
  ]

  tags = {
    Team = "team1"
    Name = "team1-redis-subnet-group"
  }
}

resource "aws_elasticache_replication_group" "team1_redis" {
  replication_group_id = "team1-truss-redis"
  description          = "team1 Truss orderbook cache (primary + 1 replica, Multi-AZ failover)"

  engine         = "redis"
  engine_version = "7.1"
  node_type      = "cache.t4g.medium"
  port           = 6379

  num_cache_clusters         = 2 # 프라이머리 1 + 레플리카 1
  automatic_failover_enabled = true
  multi_az_enabled           = true

  # transit 암호화를 켜면 클라이언트는 rediss://(TLS) 스킴으로 접속해야 한다.
  # auth_token은 transit_encryption_enabled=true가 전제조건(AWS 요구사항).
  at_rest_encryption_enabled = true
  transit_encryption_enabled = true
  auth_token                 = random_password.redis_auth.result

  subnet_group_name  = aws_elasticache_subnet_group.team1_redis.name
  security_group_ids = [data.terraform_remote_state.network.outputs.security_group_ids.redis]

  tags = {
    Team = "team1"
    Name = "team1-truss-redis"
  }
}

# mysql-ec2.tf의 mysql_root_password와 같은 이유 — redis-cli -a로 직접 붙어 디버깅할 때 필요.
output "redis_auth_token" {
  value     = random_password.redis_auth.result
  sensitive = true
}
