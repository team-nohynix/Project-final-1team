# 라이프사이클 규칙 없음 — 같은 시뮬레이션 데이터로 인프라 변경 전후 성능을 비교해야 하므로
# 만료/전환 금지. 퍼블릭 액세스는 전면 차단(frontend는 CloudFront OAC로만 접근).

resource "aws_s3_bucket" "market_data" {
  bucket = "team1-truss-market-data"

  tags = {
    Team = "team1"
    Name = "team1-truss-market-data"
  }

  lifecycle {
    prevent_destroy = true
  }
}

resource "aws_s3_bucket_server_side_encryption_configuration" "market_data" {
  bucket = aws_s3_bucket.market_data.id

  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm = "AES256"
    }
  }
}

resource "aws_s3_bucket_public_access_block" "market_data" {
  bucket = aws_s3_bucket.market_data.id

  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

# order-records·trade-results는 리플레이 입력/체결 원본이라 버저닝 추가.

resource "aws_s3_bucket" "order_records" {
  bucket = "team1-truss-order-records"

  tags = {
    Team = "team1"
    Name = "team1-truss-order-records"
  }

  lifecycle {
    prevent_destroy = true
  }
}

resource "aws_s3_bucket_server_side_encryption_configuration" "order_records" {
  bucket = aws_s3_bucket.order_records.id

  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm = "AES256"
    }
  }
}

resource "aws_s3_bucket_public_access_block" "order_records" {
  bucket = aws_s3_bucket.order_records.id

  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_s3_bucket_versioning" "order_records" {
  bucket = aws_s3_bucket.order_records.id

  versioning_configuration {
    status = "Enabled"
  }
}

resource "aws_s3_bucket" "trade_results" {
  bucket = "team1-truss-trade-results"

  tags = {
    Team = "team1"
    Name = "team1-truss-trade-results"
  }

  lifecycle {
    prevent_destroy = true
  }
}

resource "aws_s3_bucket_server_side_encryption_configuration" "trade_results" {
  bucket = aws_s3_bucket.trade_results.id

  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm = "AES256"
    }
  }
}

resource "aws_s3_bucket_public_access_block" "trade_results" {
  bucket = aws_s3_bucket.trade_results.id

  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_s3_bucket_versioning" "trade_results" {
  bucket = aws_s3_bucket.trade_results.id

  versioning_configuration {
    status = "Enabled"
  }
}

# CloudFront가 OAC로만 접근 — 버킷 자체는 퍼블릭 액세스 차단(정책은 edge.tf에서 부여).
resource "aws_s3_bucket" "frontend" {
  bucket = "team1-truss-frontend"

  tags = {
    Team = "team1"
    Name = "team1-truss-frontend"
  }

  lifecycle {
    prevent_destroy = true
  }
}

resource "aws_s3_bucket_server_side_encryption_configuration" "frontend" {
  bucket = aws_s3_bucket.frontend.id

  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm = "AES256"
    }
  }
}

resource "aws_s3_bucket_public_access_block" "frontend" {
  bucket = aws_s3_bucket.frontend.id

  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}
