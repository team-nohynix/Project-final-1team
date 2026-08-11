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
