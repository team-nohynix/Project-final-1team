# AWS Secrets Manager — 2026-08-27, recorder의 DATABASE_URL을 Secrets Store CSI
# Driver(csi-secrets-store.tf)로 동기화하기 위해 만든 시크릿. 값은 mysql-ec2.tf가 이미
# kubernetes_secret.recorder_db에 쓰던 것과 완전히 같은 표현식을 재사용한다 — 새 값을
# 코드에 하드코딩하지 않고, 같은 root_password/private_ip 참조를 그대로 쓰는 것.
#
# 이전에 있던 team1/backend/secrets, team1/monitoring/grafana-admin 두 시크릿은 terraform
# 관리 밖에서 손으로 만들어졌던 것이고(state에 없음), 2026-07-28에 소프트 삭제된 채로
# 남아 있다가 이번 정리에서 영구 삭제했다 — 이 파일의 관리 대상이 아니다.
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
