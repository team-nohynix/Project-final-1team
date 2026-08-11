# 컨테이너 이미지 저장소 1개, 컴포넌트는 태그로 구분(ingest-api-latest, collector-v3 등).
# Pull 권한은 AmazonEKSFargatePodExecutionRolePolicy/AmazonEC2ContainerRegistryReadOnly에
# 이미 포함돼 있어 추가 IAM 불필요.

resource "aws_ecr_repository" "team1" {
  name                 = "team1-truss"
  image_tag_mutability = "MUTABLE"

  image_scanning_configuration {
    scan_on_push = true
  }

  encryption_configuration {
    encryption_type = "AES256"
  }

  tags = {
    Team = "team1"
    Name = "team1-truss"
  }
}

resource "aws_ecr_lifecycle_policy" "team1" {
  repository = aws_ecr_repository.team1.name

  policy = jsonencode({
    rules = [{
      rulePriority = 1
      description  = "Expire untagged images after 7 days"
      selection = {
        tagStatus   = "untagged"
        countType   = "sinceImagePushed"
        countUnit   = "days"
        countNumber = 7
      }
      action = {
        type = "expire"
      }
    }]
  })
}
