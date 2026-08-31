#!/bin/bash
set -euo pipefail

dnf install -y docker
systemctl enable --now docker
curl -SL https://github.com/docker/compose/releases/download/v2.30.3/docker-compose-linux-x86_64 -o /usr/local/bin/docker-compose
chmod +x /usr/local/bin/docker-compose

mkdir -p /etc/prometheus /etc/grafana/provisioning/datasources /etc/grafana/provisioning/dashboards

# EKS를 이 EC2와 같은 terraform apply로 새로 만든 직후라면, team1-monitoring-role의
# IAM 정책이 이 인스턴스가 뜨는 시점까지 전파가 안 끝났을 수 있다 — set -euo pipefail
# 아래에서 이 첫 AWS API 호출이 곧바로 실패하면 스크립트 전체가 거기서 멈추고 뒤의
# docker-compose까지 전부 안 돈다(컨테이너는 하나도 안 뜬 채로 부팅이 "성공"만 표시됨).
# 최대 5분(10회×30초) 재시도.
retry_aws() {
  local attempt=1 max_attempts=10
  until "$@"; do
    if [ "$attempt" -ge "$max_attempts" ]; then
      echo "AWS API 호출이 $${max_attempts}회 재시도 후에도 실패: $*" >&2
      return 1
    fi
    echo "AWS API 호출 실패(IAM 전파 지연일 수 있음), 30초 후 재시도 ($attempt/$max_attempts): $*" >&2
    attempt=$((attempt + 1))
    sleep 30
  done
}

# EKS API 서버 CA(1회만 필요, 회전 안 됨)와 IAM 인증 토큰(약 14분 후 만료)을 파일로 만들어
# Prometheus의 authorization.credentials_file/tls_config.ca_file로 직접 읽게 한다(디스커버리+
# 스크레이프 둘 다). kubeconfig의 exec 플러그인(aws eks get-token) 방식은 처음 써봤는데
# Prometheus 공식 이미지 컨테이너 안에 aws-cli가 없어서 동작하지 않았다 — 정적 토큰 파일을
# systemd 타이머로 갱신하는 쪽으로 변경(cron/nohup 백그라운드 루프 대신 systemd라
# `systemctl status`/`journalctl`로 상태를 바로 들여다볼 수 있음).
retry_aws aws eks describe-cluster --region ${aws_region} --name ${eks_cluster_name} \
  --query 'cluster.certificateAuthority.data' --output text > /tmp/eks-ca.b64
base64 -d /tmp/eks-ca.b64 > /etc/prometheus/eks-ca.crt

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
retry_aws /usr/local/bin/refresh-eks-token.sh # 컨테이너 기동 전에 첫 토큰 파일을 미리 만들어 둠 — 위 retry_aws와 같은 IAM 전파 지연 대비

# 모든 설정 파일은 S3(team1-monitoring-config)에서 받아온다 — 대시보드 JSON을
# 포함해 user_data에 통째로 박으면 EC2의 16,384바이트 한도를 넘긴다. S3로 옮기면
# user_data는 이 스크립트만 남아 한도에 걸릴 일이 없고, 설정 내용이 바뀌어도(S3
# 객체만 갱신) EC2가 재생성되지 않는다 — infra/monitoring-ec2.tf의
# monitoring_user_data 참고.
aws s3 cp "s3://${config_bucket}/prometheus.yml" /etc/prometheus/prometheus.yml
aws s3 cp "s3://${config_bucket}/datasource.yml" /etc/grafana/provisioning/datasources/datasource.yml
aws s3 cp "s3://${config_bucket}/provider.yml" /etc/grafana/provisioning/dashboards/provider.yml
aws s3 cp "s3://${config_bucket}/team1-overview.json" /etc/grafana/provisioning/dashboards/team1-overview.json
aws s3 cp "s3://${config_bucket}/system-overview.json" /etc/grafana/provisioning/dashboards/system-overview.json
aws s3 cp "s3://${config_bucket}/docker-compose.yml" /etc/docker-compose.yml

cd /etc && docker-compose -f docker-compose.yml up -d
