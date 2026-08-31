# Secrets Store CSI Driver + AWS Provider — recorder의 DATABASE_URL을 AWS Secrets
# Manager(team1/backend/mysql-db-url, secrets-manager.tf)에서 동기화한다. 드라이버 자체는
# kube-system에 이 helm_release로 뜨고, secretObjects sync 블록으로 recorder-db-secret이라는
# 이름의 K8s Secret을 만들어주므로 recorder-deployment.yaml의 secretKeyRef는 그대로 쓴다.
resource "helm_release" "csi_secrets_store" {
  name             = "csi-secrets-store"
  namespace        = "kube-system"
  repository       = "https://kubernetes-sigs.github.io/secrets-store-csi-driver/charts"
  chart            = "secrets-store-csi-driver"
  version          = "1.6.0"
  create_namespace = false

  values = [yamlencode({
    enableSecretRotation = true
    syncSecret           = { enabled = true }
    linux = {
      # CRD 설치용 pre-install Job이 기본 톨러레이션(operator: Exists)을 그대로 물려받아
      # Fargate 노드(collector/ai-trader/replay용)에도 스케줄되면서
      # "SchedulerName is not fargate-scheduler"로 실패한다 — 시스템 노드그룹에 고정.
      crds = {
        nodeSelector = {
          "eks.amazonaws.com/nodegroup" = aws_eks_node_group.system.node_group_name
        }
      }
      # 드라이버 DaemonSet 자체는 recorder가 실제로 뜨는 Karpenter 노드(team1-backend/
      # team1-recorder)에도 등록돼야 하므로 시스템 노드그룹으로 고정하면 안 되고,
      # Fargate만 제외한다.
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

# 이 차트가 만드는 CSIDriver(secrets-store.csi.k8s.io)는 spec.tokenRequests가 비어 있어서
# (차트에 이 값을 노출하는 values가 없음) 마운트 시 "CSI token error: serviceAccount.tokens
# not provided"로 실패한다. kubernetes_csi_driver_v1로 같은 이름의 리소스를 새로 선언하면
# helm이 이미 만든 오브젝트와 소유권이 충돌하므로, helm_release 적용 뒤 패치로 보완한다.
#
# providers.tf의 kubernetes/helm provider는 exec{}로 매 호출마다 aws eks get-token을
# 그때그때 부르는 방식이라 ~/.kube/config 자체가 없다 — local-exec 안에서 커맨드 스스로
# kubeconfig를 만들어 써야 bare kubectl이 인증된다.
resource "null_resource" "csi_driver_token_requests_patch" {
  triggers = {
    helm_revision = helm_release.csi_secrets_store.metadata[0].revision
  }

  provisioner "local-exec" {
    command     = <<-EOT
      export KUBECONFIG=$(mktemp)
      aws eks update-kubeconfig --name ${aws_eks_cluster.team1.name} --region ap-northeast-2
      kubectl patch csidriver secrets-store.csi.k8s.io --type=merge -p \
        '{"spec":{"tokenRequests":[{"audience":"sts.amazonaws.com","expirationSeconds":86400}]}}'
    EOT
    interpreter = ["bash", "-c"]
  }

  depends_on = [helm_release.csi_secrets_store]
}
