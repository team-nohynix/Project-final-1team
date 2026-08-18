# ElastiCache Redis — 프라이머리(data-a)+레플리카(data-b), 호가창 현재 상태 캐시 전용.
# cache.t4g.medium(버스터블) — 리플레이 고배속 구간 CPU 크레딧 소진 여부는 부하 시험에서 확인.

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
  at_rest_encryption_enabled = true
  transit_encryption_enabled = true

  subnet_group_name  = aws_elasticache_subnet_group.team1_redis.name
  security_group_ids = [data.terraform_remote_state.network.outputs.security_group_ids.redis]

  tags = {
    Team = "team1"
    Name = "team1-truss-redis"
  }
}
