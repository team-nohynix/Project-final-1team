# backend 네임스페이스 — 원래 infra/k8s/backend/namespace.yaml을 CI가 kubectl apply로
# 만들었는데(지금도 그렇게 함, 중복 생성 시도는 무해함 — kubectl apply는 이미 있는
# 네임스페이스에 그냥 멱등하게 적용됨), recorder-db-secret(kubernetes_secret, 아래
# mysql-ec2.tf)이 이 네임스페이스가 존재해야만 만들어질 수 있어서 terraform 안에서도
# 하나 갖고 있어야 한다 — CI의 kubectl apply 순서에 기대지 않고 terraform apply
# 하나만으로 시크릿까지 자기완결적으로 만들어지게 하려는 목적(2026-08-24).
resource "kubernetes_namespace" "backend" {
  depends_on = [time_sleep.wait_for_eks_auth]

  metadata {
    name   = "backend"
    labels = { team = "team1" }
  }
}

# backend-cluster-config — MSK Serverless의 부트스트랩 브로커 주소를 담는다
# (2026-08-24, EKS 전체 destroy→apply 리허설 중 발견해서 추가). 이 주소는
# `boot-<임의 해시>.c1.kafka-serverless...` 형태로 클러스터를 새로 만들 때마다
# 바뀌는데, orderapi/matching-engine/recorder-deployment.yaml과
# kafka-admin-job.yaml 4곳에 리터럴 문자열로 박혀 있어서 MSK를 재생성할 때마다
# 전부 손으로 고쳐야 했다(실제로 리허설 중 이것 때문에 전부 연결 실패했음).
# recorder-db-secret과 같은 이유로 kubernetes_config_map으로 옮겨서, 그
# 4개 매니페스트는 이제 리터럴 값 대신 이 ConfigMap을 참조한다 — terraform
# apply 한 번이면 항상 최신 브로커 주소로 맞춰진다.
resource "kubernetes_config_map" "backend_cluster_config" {
  metadata {
    name      = "backend-cluster-config"
    namespace = kubernetes_namespace.backend.metadata[0].name
  }

  data = {
    KAFKA_BROKER = aws_msk_serverless_cluster.team1_truss.bootstrap_brokers_sasl_iam
  }
}

# 클러스터 부트스트랩용 Helm 애드온(KEDA/kube-state-metrics/node-exporter) — 원래
# `helm install`로 손으로 설치돼 있던 것을 "EKS 클러스터를 통째로 지웠다 올려도
# 원상복구되게"(2026-08-24, 사용자 요청) terraform으로 옮겼다. alb-controller.tf의
# aws_load_balancer_controller, karpenter.tf의 karpenter와 같은 이유 — IAM(IRSA)이
# 필요 없는 애드온이라 여기 한 파일에 모았다. 셋 다 `helm get values <release> -n
# <namespace>`로 확인한 실제 사용자 지정값이 전혀 없었다(전부 차트 기본값 그대로
# 설치돼 있었음, 2026-08-24 확인) — 그래서 values 블록 없이 차트+버전만 지정한다.

resource "helm_release" "keda" {
  name             = "keda"
  namespace        = "keda"
  repository       = "https://kedacore.github.io/charts"
  chart            = "keda"
  version          = "2.20.2" # helm list -A로 확인한 실제 배포 버전(2026-08-24)
  create_namespace = true

  depends_on = [aws_eks_node_group.system, time_sleep.wait_for_eks_auth, helm_release.aws_load_balancer_controller]
}

resource "helm_release" "kube_state_metrics" {
  name             = "kube-state-metrics"
  namespace        = "monitoring"
  repository       = "https://prometheus-community.github.io/helm-charts"
  chart            = "kube-state-metrics"
  version          = "8.4.0" # helm list -A로 확인한 실제 배포 버전(2026-08-24)
  create_namespace = true

  # 차트 기본값은 Service에 prometheus.io/scrape=true만 붙이고 port/path
  # annotation은 안 붙인다. monitoring-ec2/prometheus.yml.tpl의 k8s-services
  # job은 이 둘이 다 있어야 파드 프록시 경로(/api/v1/namespaces/.../pods/
  # ...:$port/proxy$path)를 만드는데, 없으면 그 relabel 규칙이 통째로
  # 스킵되고 __address__(EKS API 서버 자체)에 Prometheus 기본 경로("/metrics")로
  # 그대로 스크레이프해버린다 — 그 결과 EKS API 서버 자신의 내부 메트릭
  # (aggregator_discovery_* 등)을 kube-state-metrics 데이터인 것처럼 성공
  # (up=1)으로 오인하고, 정작 kube_pod_*/kube_deployment_*/kube_node_*는
  # 전혀 안 들어와서 이걸 쓰는 그라파나 패널이 전부 "No data"가 된다
  # (2026-08-25, EKS 전체 destroy→apply 리허설 후 그라파나에서 실측 발견).
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
  version          = "4.56.1" # helm list -A로 확인한 실제 배포 버전(2026-08-24)
  create_namespace = true

  # kube_state_metrics 바로 위 주석과 같은 이유 — 이 차트도 기본값으로는
  # port/path annotation을 안 붙인다.
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
