# backend 네임스페이스 — infra/k8s/backend/namespace.yaml을 CI가 kubectl apply로도
# 만들지만(중복 생성 시도는 무해), recorder-db-secret(kubernetes_secret, mysql-ec2.tf)이
# 이 네임스페이스가 존재해야만 만들어질 수 있어서 terraform 안에서도 하나 갖고 있다 —
# CI의 kubectl apply 순서에 기대지 않고 terraform apply 하나만으로 시크릿까지
# 자기완결적으로 만들어지게 하려는 목적.
resource "kubernetes_namespace" "backend" {
  depends_on = [time_sleep.wait_for_eks_auth]

  metadata {
    name   = "backend"
    labels = { team = "team1" }
  }
}

# backend-cluster-config — MSK Serverless의 부트스트랩 브로커 주소를 담는다. 이 주소는
# `boot-<임의 해시>.c1.kafka-serverless...` 형태로 클러스터를 새로 만들 때마다 바뀌는데,
# orderapi/matching-engine/recorder-deployment.yaml과 kafka-admin-job.yaml 4곳이 이
# 값을 쓴다. recorder-db-secret과 같은 이유로 kubernetes_config_map으로 관리해서, 그
# 4개 매니페스트는 리터럴 값 대신 이 ConfigMap을 참조한다 — terraform apply 한 번이면
# 항상 최신 브로커 주소로 맞춰진다.
resource "kubernetes_config_map" "backend_cluster_config" {
  metadata {
    name      = "backend-cluster-config"
    namespace = kubernetes_namespace.backend.metadata[0].name
  }

  data = {
    KAFKA_BROKER = aws_msk_serverless_cluster.team1_truss.bootstrap_brokers_sasl_iam
  }
}

# 클러스터 부트스트랩용 Helm 애드온(KEDA/kube-state-metrics/node-exporter) — IAM(IRSA)이
# 필요 없는 애드온이라 여기 한 파일에 모았다. 셋 다 차트 기본값 그대로 설치한다.

resource "helm_release" "keda" {
  name             = "keda"
  namespace        = "keda"
  repository       = "https://kedacore.github.io/charts"
  chart            = "keda"
  version          = "2.20.2"
  create_namespace = true

  depends_on = [aws_eks_node_group.system, time_sleep.wait_for_eks_auth, helm_release.aws_load_balancer_controller]
}

resource "helm_release" "kube_state_metrics" {
  name             = "kube-state-metrics"
  namespace        = "monitoring"
  repository       = "https://prometheus-community.github.io/helm-charts"
  chart            = "kube-state-metrics"
  version          = "8.4.0"
  create_namespace = true

  # 차트 기본값은 Service에 prometheus.io/scrape=true만 붙이고 port/path annotation은
  # 안 붙인다. monitoring-ec2/prometheus.yml.tpl의 k8s-services job은 이 둘이 다 있어야
  # 파드 프록시 경로를 만드는데, 없으면 그 relabel 규칙이 스킵되고 EKS API 서버 자체를
  # kube-state-metrics인 것처럼 잘못 스크레이프한다 — kube_pod_*/kube_deployment_*
  # 계열이 전부 안 들어와서 그라파나 패널이 "No data"가 된다.
  values = [yamlencode({
    service = {
      annotations = {
        "prometheus.io/port" = "8080"
        "prometheus.io/path" = "/metrics"
      }
    }
  })]

  depends_on = [aws_eks_node_group.system, time_sleep.wait_for_eks_auth, helm_release.aws_load_balancer_controller]
}

resource "helm_release" "node_exporter" {
  name             = "node-exporter"
  namespace        = "monitoring"
  repository       = "https://prometheus-community.github.io/helm-charts"
  chart            = "prometheus-node-exporter"
  version          = "4.56.1"
  create_namespace = true

  # kube_state_metrics 바로 위 주석과 같은 이유 — 이 차트도 기본값으로는 port/path
  # annotation을 안 붙인다.
  values = [yamlencode({
    service = {
      annotations = {
        "prometheus.io/port" = "9100"
        "prometheus.io/path" = "/metrics"
      }
    }
  })]

  depends_on = [aws_eks_node_group.system, time_sleep.wait_for_eks_auth, helm_release.aws_load_balancer_controller]
}
