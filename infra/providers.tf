terraform {
  required_version = ">= 1.5.0"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 5.0"
    }
    tls = {
      source  = "hashicorp/tls"
      version = "~> 4.0"
    }
    archive = {
      source  = "hashicorp/archive"
      version = "~> 2.4"
    }
    random = {
      source  = "hashicorp/random"
      version = "~> 3.6"
    }
    kubernetes = {
      source  = "hashicorp/kubernetes"
      version = "~> 2.31"
    }
    helm = {
      source  = "hashicorp/helm"
      version = "~> 2.15"
    }
  }

  backend "s3" {
    bucket       = "team1-terraform-state-s3"
    key          = "truss/terraform.tfstate"
    region       = "ap-northeast-2"
    use_lockfile = true
  }
}

provider "aws" {
  region = "ap-northeast-2"
}

# CloudFront ACM 인증서는 us-east-1 전용(edge.tf에서 사용).
provider "aws" {
  alias  = "us_east_1"
  region = "us-east-1"
}

# kubernetes/helm 프로바이더 인증 (2026-08-24, "EKS 통째로 지웠다 올려도 복구되게"
# 작업의 일부) — recorder-db-secret(kubernetes_secret)과 karpenter/aws-load-balancer-
# controller/keda/kube-state-metrics/node-exporter(helm_release, helm-releases.tf)가
# 이 둘을 씁니다. static token이 아니라 exec 플러그인으로 매 실행 시 `aws eks
# get-token`을 새로 호출합니다 — EKS 토큰은 15분 만료라 terraform apply 시점에 늘 새로
# 받아야 하고, CI(terraform-apply job)도 이미 같은 IAM 역할(TF_APPLY_ROLE)로 AWS
# 자격증명을 갖고 있으니 별도 설정 없이 그 자격증명을 그대로 씁니다.
provider "kubernetes" {
  host                   = aws_eks_cluster.team1.endpoint
  cluster_ca_certificate = base64decode(aws_eks_cluster.team1.certificate_authority[0].data)

  exec {
    api_version = "client.authentication.k8s.io/v1beta1"
    command     = "aws"
    args        = ["eks", "get-token", "--cluster-name", aws_eks_cluster.team1.name, "--region", "ap-northeast-2"]
  }
}

provider "helm" {
  kubernetes {
    host                   = aws_eks_cluster.team1.endpoint
    cluster_ca_certificate = base64decode(aws_eks_cluster.team1.certificate_authority[0].data)

    exec {
      api_version = "client.authentication.k8s.io/v1beta1"
      command     = "aws"
      args        = ["eks", "get-token", "--cluster-name", aws_eks_cluster.team1.name, "--region", "ap-northeast-2"]
    }
  }
}

data "aws_caller_identity" "current" {}

# network 스택(infra/network/, key "network/terraform.tfstate") 출력 참조.
data "terraform_remote_state" "network" {
  backend = "s3"

  config = {
    bucket = "team1-terraform-state-s3"
    key    = "network/terraform.tfstate"
    region = "ap-northeast-2"
  }
}
