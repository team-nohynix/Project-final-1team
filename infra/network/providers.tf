# 독립 state — VPC 재생성 같은 파괴적 변경을 EKS/RDS 등(../*.tf)과 분리.
terraform {
  required_version = ">= 1.5.0"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 5.0"
    }
  }

  backend "s3" {
    bucket       = "team1-terraform-state-s3"
    key          = "network/terraform.tfstate"
    region       = "ap-northeast-2"
    use_lockfile = true
  }
}

provider "aws" {
  region = "ap-northeast-2"
}
