# AWS Secrets Manager — recorder의 DATABASE_URL을 Secrets Store CSI Driver
# (csi-secrets-store.tf)로 동기화하기 위해 만든 시크릿. 값은 mysql-ec2.tf의
# kubernetes_secret.recorder_db와 같은 root_password/private_ip 참조를 그대로 쓴다.
resource "aws_secretsmanager_secret" "recorder_mysql_db_url" {
  name        = "team1/backend/mysql-db-url"
  description = "recorder의 DATABASE_URL(go-sql-driver/mysql DSN) — Secrets Store CSI Driver가 backend/recorder-db-secret K8s Secret으로 동기화한다."

  tags = { Team = "team1", Name = "team1-secret-recorder-mysql-db-url" }
}

resource "aws_secretsmanager_secret_version" "recorder_mysql_db_url" {
  secret_id = aws_secretsmanager_secret.recorder_mysql_db_url.id
  secret_string = jsonencode({
    # mysql-ec2.tf의 kubernetes_secret.recorder_db와 동일한 DSN 형식 —
    # SecretProviderClass(infra/k8s/backend/recorder-db-secret-provider.yaml)가
    # jmesPath로 이 "DATABASE_URL" 키를 그대로 뽑아 K8s Secret 키에 매핑한다.
    DATABASE_URL = "root:${random_password.mysql_root.result}@tcp(${aws_instance.mysql.private_ip}:3306)/team1_truss?parseTime=true&loc=UTC"
  })
}

# Redis AUTH 토큰(elasticache.tf). orderapi/matching/recorder 셋 다 이 시크릿을
# SecretProviderClass(infra/k8s/backend/redis-auth-secret-provider.yaml)로 동기화해서
# REDIS_PASSWORD 환경변수로 받는다 — 위 recorder_mysql_db_url과 같은 패턴.
resource "aws_secretsmanager_secret" "redis_auth_token" {
  name        = "team1/backend/redis-auth-token"
  description = "ElastiCache(team1-truss-redis) AUTH 토큰 — Secrets Store CSI Driver가 backend/redis-auth-secret K8s Secret으로 동기화한다."

  tags = { Team = "team1", Name = "team1-secret-redis-auth-token" }
}

resource "aws_secretsmanager_secret_version" "redis_auth_token" {
  secret_id = aws_secretsmanager_secret.redis_auth_token.id
  secret_string = jsonencode({
    REDIS_PASSWORD = random_password.redis_auth.result
  })
}
