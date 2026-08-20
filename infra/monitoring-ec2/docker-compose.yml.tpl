services:
  prometheus:
    image: prom/prometheus:v2.54.1
    container_name: prometheus
    restart: unless-stopped
    volumes:
      - /etc/prometheus/prometheus.yml:/etc/prometheus/prometheus.yml:ro
      - /etc/prometheus/eks-ca.crt:/etc/prometheus/eks-ca.crt:ro
      - /etc/prometheus/eks-token:/etc/prometheus/eks-token:ro
      - prometheus-data:/prometheus
    command:
      - --config.file=/etc/prometheus/prometheus.yml
      - --storage.tsdb.retention.time=3d
      - --web.enable-lifecycle
    ports:
      - "9090:9090"

  grafana:
    image: grafana/grafana:11.2.0
    container_name: grafana
    restart: unless-stopped
    environment:
      GF_SECURITY_ADMIN_PASSWORD: "${grafana_admin_password}"
      GF_AUTH_ANONYMOUS_ENABLED: "true"
      GF_AUTH_ANONYMOUS_ORG_ROLE: "Viewer"
      GF_SECURITY_ALLOW_EMBEDDING: "true"
      # 2026-08-20: 파이프라인 실시간 흐름 시각화(팀1 AI Trader 대시보드의
      # "실시간 처리 흐름" 패널)용 — Grafana의 Text 패널은 기본적으로 HTML을
      # sanitize해서 <script>/onerror 등 인라인 JS를 다 지운다. 이 값을 켜야
      # 그 패널 안의 캔버스 애니메이션 코드가 실제로 실행된다. 전역 설정이라
      # 이 인스턴스의 어떤 Text 패널이든 임의 JS를 심을 수 있게 되는
      # 트레이드오프가 있음 — 이 Grafana는 팀 전용이고 익명 Viewer만 접근
      # 가능해서(대시보드 편집 권한 없음) 감수하기로 함.
      GF_PANELS_DISABLE_SANITIZE_HTML: "true"
    volumes:
      - /etc/grafana/provisioning:/etc/grafana/provisioning:ro
      - grafana-data:/var/lib/grafana
    ports:
      - "3000:3000"
      - "80:3000" # monitor.jhyang.click 클릭 시 포트 없이 바로 열리도록

volumes:
  prometheus-data:
  grafana-data:
