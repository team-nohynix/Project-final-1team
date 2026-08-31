global:
  scrape_interval: 30s
  evaluation_interval: 30s

scrape_configs:
  - job_name: prometheus-self
    static_configs:
      - targets: ["localhost:9090"]

  # 클러스터 안 Service(annotation prometheus.io/scrape=true)를 EKS API 서버 프록시
  # 경유로 스크레이프한다 — 이 EC2는 EKS 노드/파드 보안그룹과 직접 신뢰관계를 맺지
  # 않고, IAM(Access Entry) + RBAC(external-prometheus ClusterRole)로만 인증한다.
  - job_name: k8s-services
    scheme: https
    tls_config:
      ca_file: /etc/prometheus/eks-ca.crt
    authorization:
      credentials_file: /etc/prometheus/eks-token
    kubernetes_sd_configs:
      - role: endpoints
        api_server: https://${eks_api_host}
        authorization:
          credentials_file: /etc/prometheus/eks-token
        tls_config:
          ca_file: /etc/prometheus/eks-ca.crt
    relabel_configs:
      - source_labels: [__meta_kubernetes_service_annotation_prometheus_io_scrape]
        action: keep
        regex: "true"
      # 서비스 단위 프록시 경로(/services/$name:$port/proxy)를 쓰면 endpoints role이
      # 레플리카(파드)마다 별도 타겟을 발견해도 relabel 후 주소가 전부 똑같아져서
      # Prometheus가 하나의 타겟으로 합쳐버린다 — 매 스크레이프마다 API 서버가
      # 그 서비스 뒤의 파드 중 하나에게 무작위로 라우팅해서, 레플리카가 2개 이상인
      # 서비스에서 sum(...)이 "둘의 합"이 아니라 "둘 중 하나"를 왔다갔다 하는 값이
      # 된다. 파드 단위 프록시 경로(/pods/$podName:$port/proxy)로 바꿔서 레플리카별로
      # 진짜 별도 타겟이 되게 한다 — target_kind가 Pod가 아닌 엔드포인트(드묾)는
      # 걸러낸다.
      - source_labels: [__meta_kubernetes_endpoint_address_target_kind]
        action: keep
        regex: Pod
      - source_labels:
          - __meta_kubernetes_namespace
          - __meta_kubernetes_endpoint_address_target_name
          - __meta_kubernetes_service_annotation_prometheus_io_port
          - __meta_kubernetes_service_annotation_prometheus_io_path
        regex: (.+);(.+);(.+);(.+)
        replacement: /api/v1/namespaces/$1/pods/$2:$3/proxy$4
        target_label: __metrics_path__
      - target_label: __address__
        replacement: ${eks_api_host}
      - source_labels: [__meta_kubernetes_namespace]
        target_label: namespace
      - source_labels: [__meta_kubernetes_service_name]
        target_label: service
      - source_labels: [__meta_kubernetes_endpoint_address_target_name]
        target_label: pod

  # 노드 cAdvisor(/metrics/cadvisor)도 같은 API 서버 프록시 경로로 스크레이프 —
  # kubelet(10250)에 직접 붙지 않으므로 노드/백엔드 보안그룹에 아무 규칙도 추가하지 않는다.
  - job_name: k8s-nodes-cadvisor
    scheme: https
    tls_config:
      ca_file: /etc/prometheus/eks-ca.crt
    authorization:
      credentials_file: /etc/prometheus/eks-token
    kubernetes_sd_configs:
      - role: node
        api_server: https://${eks_api_host}
        authorization:
          credentials_file: /etc/prometheus/eks-token
        tls_config:
          ca_file: /etc/prometheus/eks-ca.crt
    relabel_configs:
      - action: labelmap
        regex: __meta_kubernetes_node_label_(.+)
      - source_labels: [__meta_kubernetes_node_name]
        target_label: __metrics_path__
        replacement: /api/v1/nodes/$1/proxy/metrics/cadvisor
      - target_label: __address__
        replacement: ${eks_api_host}
