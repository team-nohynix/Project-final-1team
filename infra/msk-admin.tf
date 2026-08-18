# Kafka 토픽 생성/관리 전용 IRSA — 애플리케이션 워크로드(sa-ingest-api 등)는 CreateTopic
# 권한이 없다. 토픽은 Terraform으로 못 만들어서(Kafka 어드민 클라이언트 필요) 이 역할을
# backend 네임스페이스의 일회성 관리 Job이 assume해 kafka-topics.sh로 생성한다.

data "aws_iam_policy_document" "irsa_kafka_admin_assume" {
  statement {
    actions = ["sts:AssumeRoleWithWebIdentity"]
    principals {
      type        = "Federated"
      identifiers = [aws_iam_openid_connect_provider.team1_eks.arn]
    }
    condition {
      test     = "StringEquals"
      variable = "${local.eks_oidc_url_no_scheme}:sub"
      values   = ["system:serviceaccount:backend:sa-kafka-admin"]
    }
    condition {
      test     = "StringEquals"
      variable = "${local.eks_oidc_url_no_scheme}:aud"
      values   = ["sts.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "sa_kafka_admin" {
  name               = "team1-sa-kafka-admin"
  assume_role_policy = data.aws_iam_policy_document.irsa_kafka_admin_assume.json
  tags               = { Team = "team1", Name = "team1-sa-kafka-admin" }
}

data "aws_iam_policy_document" "sa_kafka_admin_policy" {
  statement {
    actions   = ["kafka-cluster:Connect", "kafka-cluster:DescribeCluster"]
    resources = [aws_msk_serverless_cluster.team1_truss.arn]
  }
  statement {
    actions = [
      "kafka-cluster:CreateTopic",
      "kafka-cluster:DescribeTopic",
      "kafka-cluster:AlterTopic",
      # DeleteTopic — 배포 초기 진단용 더미 메시지를 넣은 토픽을 지우고 재생성하는 데 필요
      # (운영 중 데이터 있는 토픽엔 쓰지 않을 것).
      "kafka-cluster:DeleteTopic",
    ]
    resources = ["${local.msk_topic_arn_prefix}/*"]
  }
}

resource "aws_iam_role_policy" "sa_kafka_admin" {
  name   = "team1-sa-kafka-admin-policy"
  role   = aws_iam_role.sa_kafka_admin.id
  policy = data.aws_iam_policy_document.sa_kafka_admin_policy.json
}
