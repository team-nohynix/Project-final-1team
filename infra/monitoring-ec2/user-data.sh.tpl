#!/bin/bash
set -euo pipefail

dnf install -y docker
systemctl enable --now docker
curl -SL https://github.com/docker/compose/releases/download/v2.30.3/docker-compose-linux-x86_64 -o /usr/local/bin/docker-compose
chmod +x /usr/local/bin/docker-compose

mkdir -p /etc/prometheus /etc/grafana/provisioning/datasources /etc/grafana/provisioning/dashboards

# EKS API 서버 CA(1회만 필요, 회전 안 됨)와 IAM 인증 토큰(약 14분 후 만료)을 파일로 만들어
# Prometheus의 authorization.credentials_file/tls_config.ca_file로 직접 읽게 한다(디스커버리+
# 스크레이프 둘 다). kubeconfig의 exec 플러그인(aws eks get-token) 방식은 처음 써봤는데
# Prometheus 공식 이미지 컨테이너 안에 aws-cli가 없어서 동작하지 않았다 — 정적 토큰 파일을
# systemd 타이머로 갱신하는 쪽으로 변경(cron/nohup 백그라운드 루프 대신 systemd라
# `systemctl status`/`journalctl`로 상태를 바로 들여다볼 수 있음).
aws eks describe-cluster --region ${aws_region} --name ${eks_cluster_name} \
  --query 'cluster.certificateAuthority.data' --output text | base64 -d > /etc/prometheus/eks-ca.crt

cat > /usr/local/bin/refresh-eks-token.sh <<'TOKENSCRIPTEOF'
#!/bin/bash
set -euo pipefail
# 같은 파일에 직접 덮어쓴다(inode 유지) — mv로 교체하면 도커의 단일 파일 바인드 마운트가
# 새 inode를 못 따라가서 컨테이너 쪽에는 최초 토큰이 영원히 고정되는 문제가 있다.
aws eks get-token --region ${aws_region} --cluster-name ${eks_cluster_name} \
  --query 'status.token' --output text > /etc/prometheus/eks-token
chmod 644 /etc/prometheus/eks-token
TOKENSCRIPTEOF
chmod +x /usr/local/bin/refresh-eks-token.sh

cat > /etc/systemd/system/refresh-eks-token.service <<'TOKENSVCEOF'
[Unit]
Description=Refresh EKS IAM bearer token for external Prometheus

[Service]
Type=oneshot
ExecStart=/usr/local/bin/refresh-eks-token.sh
TOKENSVCEOF

cat > /etc/systemd/system/refresh-eks-token.timer <<'TOKENTIMEREOF'
[Unit]
Description=Run refresh-eks-token every 10 minutes

[Timer]
OnBootSec=0
OnUnitActiveSec=10min

[Install]
WantedBy=timers.target
TOKENTIMEREOF

systemctl daemon-reload
systemctl enable --now refresh-eks-token.timer
/usr/local/bin/refresh-eks-token.sh # 컨테이너 기동 전에 첫 토큰 파일을 미리 만들어 둠

cat > /etc/prometheus/prometheus.yml <<'PROMEOF'
${prometheus_yml}
PROMEOF

cat > /etc/grafana/provisioning/datasources/datasource.yml <<'DSEOF'
${grafana_datasource_yml}
DSEOF

cat > /etc/grafana/provisioning/dashboards/provider.yml <<'PROVEOF'
${grafana_dashboard_provider_yml}
PROVEOF

cat > /etc/grafana/provisioning/dashboards/team1-overview.json <<'DASHEOF'
${grafana_dashboard_json}
DASHEOF

cat > /etc/docker-compose.yml <<'COMPOSEEOF'
${docker_compose_yml}
COMPOSEEOF

cd /etc && docker-compose -f docker-compose.yml up -d
