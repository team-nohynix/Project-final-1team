#!/bin/bash
set -euo pipefail

dnf install -y docker
systemctl enable --now docker
curl -SL https://github.com/docker/compose/releases/download/v2.30.3/docker-compose-linux-x86_64 -o /usr/local/bin/docker-compose
chmod +x /usr/local/bin/docker-compose

mkdir -p /etc/mysql-init

# monitoring-ec2/user-data.sh.tpl과 같은 이유(user_data 16KB 한도) — 설정 파일은
# S3에서 받아온다. docker-compose.yml.tpl 참고: 최초 기동(데이터 디렉터리가 비어있을
# 때)에만 schema.sql이 자동 실행되므로 RDS 때처럼 수동 적용이 필요 없다.
aws s3 cp "s3://${config_bucket}/docker-compose.yml" /etc/docker-compose.yml
aws s3 cp "s3://${config_bucket}/schema.sql" /etc/mysql-init/schema.sql

cd /etc && docker-compose -f docker-compose.yml up -d
