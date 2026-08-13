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
    volumes:
      - /etc/grafana/provisioning:/etc/grafana/provisioning:ro
      - grafana-data:/var/lib/grafana
    ports:
      - "3000:3000"

volumes:
  prometheus-data:
  grafana-data:
