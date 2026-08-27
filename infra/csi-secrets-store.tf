# Secrets Store CSI Driver + AWS Provider — 2026-08-27, recorder의 DATABASE_URL을
# 손으로 만든 K8s Secret(kubernetes_secret.recorder_db, mysql-ec2.tf)에서 AWS Secrets
# Manager(team1/backend/mysql-db-url, secrets-manager.tf)로 옮기면서 라이브로 먼저
# 설치했던 것을 그대로 코드에 백필한다. 드라이버 자체는 kube-system에 이 helm_release로
# 뜨고, secretObjects sync 블록으로 여전히 kubernetes_secret과 같은 이름(recorder-db-secret)의
# K8s Secret을 만들어주므로 recorder-deployment.yaml의 secretKeyRef는 안 바꿔도 된다 —
# CSI가 그 Secret의 "생성자"만 바뀌는 것.
resource "helm_release" "csi_secrets_store" {
  name             = "csi-secrets-store"
  namespace        = "kube-system"
  repository       = "https://kubernetes-sigs.github.io/secrets-store-csi-driver/charts"
  chart            = "secrets-store-csi-driver"
  version          = "1.6.0" # helm list -A로 확인한 실제 배포 버전(2026-08-27)
  create_namespace = false

  values = [yamlencode({
    enableSecretRotation = true
    syncSecret           = { enabled = true }
    linux = {
      # CRD 설치용 pre-install Job이 기본 톨러레이션(operator: Exists)을 그대로 물려받아
      # Fargate 노드(collector/ai-trader/replay용)에도 스케줄되면서
      # "SchedulerName is not fargate-scheduler"로 실패했다 — 시스템 노드그룹에 고정.
      crds = {
        nodeSelector = {
          "eks.amazonaws.com/nodegroup" = aws_eks_node_group.system.node_group_name
        }
      }
      # 드라이버 DaemonSet 자체는 recorder가 실제로 뜨는 Karpenter 노드(team1-backend/
      # team1-recorder)에도 등록돼야 하므로 시스템 노드그룹으로 고정하면 안 되고,
      # Fargate만 제외한다 — "driver name secrets-store.csi.k8s.io not found"로 실측.
      nodeSelector = {
        "kubernetes.io/os" = "linux"
      }
      affinity = {
        nodeAffinity = {
          requiredDuringSchedulingIgnoredDuringExecution = {
            nodeSelectorTerms = [{
              matchExpressions = [{
                key      = "eks.amazonaws.com/compute-type"
                operator = "NotIn"
                values   = ["fargate"]
              }]
            }]
          }
        }
      }
    }
  })]

  depends_on = [aws_eks_node_group.system, time_sleep.wait_for_eks_auth]
}

# 위 차트가 만드는 CSIDriver(secrets-store.csi.k8s.io)는 spec.tokenRequests가 비어 있다
# (helm template로 직접 확인 — 차트에 이 값을 노출하는 values가 아예 없음). 이게 없으면
# 마운트 시 "CSI token error: serviceAccount.tokens not provided"로 실패한다(실측).
# kubernetes_csi_driver_v1로 같은 이름의 리소스를 새로 선언하면 helm이 이미 만든
# 오브젝트와 소유권이 충돌하므로(둘 다 "내가 만들었다"고 함), helm_release 적용 뒤
# 패치로 보완한다. CI 러너에 kubectl이 있어야 동작한다.
resource "null_resource" "csi_driver_token_requests_patch" {
  triggers = {
    helm_revision = helm_release.csi_secrets_store.metadata[0].revision
  }

  provisioner "local-exec" {
    command = <<-EOT
      kubectl patch csidriver secrets-store.csi.k8s.io --type=merge -p \
        '{"spec":{"tokenRequests":[{"audience":"sts.amazonaws.com","expirationSeconds":86400}]}}'
    EOT
  }

  depends_on = [helm_release.csi_secrets_store]
}
