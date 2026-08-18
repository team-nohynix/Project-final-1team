# Amazon MSK Serverless. ENI는 컴퓨트와 같은 data-a/b에만(MSK는 RDS와 달리 3AZ가 강제되지
# 않는다). 시세수집기/AI트레이더는 Kafka 대신 S3로 직접 주고받으므로, 접속하는 워크로드는
# 백엔드(접수API·매칭엔진·기록기)뿐이다. 토픽·ACL은 Kafka 어드민 클라이언트로 만들어야
# 해서 Terraform 범위 밖이다.

resource "aws_msk_serverless_cluster" "team1_truss" {
  cluster_name = "team1-truss-msk"

  vpc_config {
    subnet_ids = [
      data.terraform_remote_state.network.outputs.subnet_ids.data.a,
      data.terraform_remote_state.network.outputs.subnet_ids.data.b,
    ]
    security_group_ids = [data.terraform_remote_state.network.outputs.security_group_ids.msk]
  }

  client_authentication {
    sasl {
      iam {
        enabled = true
      }
    }
  }

  tags = {
    Team = "team1"
    Name = "team1-truss-msk"
  }
}
