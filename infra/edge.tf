# 엣지 — CloudFront+OAC(프론트엔드), WAF(Public ALB에 연결), ACM 인증서 참조.
# jhyang.click의 기존 Route53 호스팅존과 와일드카드 ACM 인증서(두 리전 모두 발급 완료)를
# 새로 만들지 않고 조회해서 재사용한다. ALB(접수 API)는 Load Balancer Controller가 Ingress
# 적용 시 생성하는 리소스라(Terraform 밖), truss-api.jhyang.click Route53 레코드는 Ingress
# 배포 이후에 추가한다. WAF는 WebACL만 만들고 ARN을 output으로 노출해 Ingress 어노테이션에서
# 참조한다.

variable "domain_name" {
  description = "CloudFront/ALB용 ACM 인증서와 별칭에 쓰는 베이스 도메인"
  type        = string
  default     = "jhyang.click"
}

locals {
  frontend_domain = "truss.${var.domain_name}"
}

data "aws_route53_zone" "team1" {
  name         = "${var.domain_name}."
  private_zone = false
}

# --- ACM (기존 와일드카드 인증서 조회, 신규 발급 없음) ----------------------

data "aws_acm_certificate" "cloudfront" {
  provider    = aws.us_east_1
  domain      = var.domain_name
  types       = ["AMAZON_ISSUED"]
  statuses    = ["ISSUED"]
  most_recent = true
}

data "aws_acm_certificate" "alb" {
  domain      = var.domain_name
  types       = ["AMAZON_ISSUED"]
  statuses    = ["ISSUED"]
  most_recent = true
}

# --- WAF (Public ALB에 연결, ARN은 Ingress 어노테이션에서 참조) -------------

resource "aws_wafv2_web_acl" "alb" {
  name        = "team1-waf-alb"
  description = "team1 ingest API Public ALB WAF"
  scope       = "REGIONAL"

  default_action {
    allow {}
  }

  rule {
    name     = "AWSManagedRulesCommonRuleSet"
    priority = 1

    override_action {
      none {}
    }

    statement {
      managed_rule_group_statement {
        name        = "AWSManagedRulesCommonRuleSet"
        vendor_name = "AWS"
      }
    }

    visibility_config {
      cloudwatch_metrics_enabled = true
      metric_name                = "team1-waf-common-rules"
      sampled_requests_enabled   = true
    }
  }

  rule {
    name     = "AWSManagedRulesKnownBadInputsRuleSet"
    priority = 2

    override_action {
      none {}
    }

    statement {
      managed_rule_group_statement {
        name        = "AWSManagedRulesKnownBadInputsRuleSet"
        vendor_name = "AWS"
      }
    }

    visibility_config {
      cloudwatch_metrics_enabled = true
      metric_name                = "team1-waf-known-bad-inputs"
      sampled_requests_enabled   = true
    }
  }

  # 부하 시험 트래픽을 막지 않도록 여유 있게 설정 — DDoS성 단일 IP 남용 방지용.
  rule {
    name     = "RateLimitPerIP"
    priority = 3

    action {
      block {}
    }

    statement {
      rate_based_statement {
        limit              = 20000
        aggregate_key_type = "IP"
      }
    }

    visibility_config {
      cloudwatch_metrics_enabled = true
      metric_name                = "team1-waf-rate-limit"
      sampled_requests_enabled   = true
    }
  }

  visibility_config {
    cloudwatch_metrics_enabled = true
    metric_name                = "team1-waf-alb"
    sampled_requests_enabled   = true
  }

  tags = {
    Team = "team1"
    Name = "team1-waf-alb"
  }
}

# --- CloudFront + OAC (프론트엔드 S3, 퍼블릭 액세스는 계속 차단) ------------

resource "aws_cloudfront_origin_access_control" "frontend" {
  name                              = "team1-oac-frontend"
  origin_access_control_origin_type = "s3"
  signing_behavior                  = "always"
  signing_protocol                  = "sigv4"
}

resource "aws_cloudfront_distribution" "frontend" {
  enabled             = true
  default_root_object = "index.html"
  aliases             = [local.frontend_domain]

  origin {
    domain_name              = aws_s3_bucket.frontend.bucket_regional_domain_name
    origin_id                = "team1-frontend-s3"
    origin_access_control_id = aws_cloudfront_origin_access_control.frontend.id
  }

  default_cache_behavior {
    allowed_methods        = ["GET", "HEAD"]
    cached_methods         = ["GET", "HEAD"]
    target_origin_id       = "team1-frontend-s3"
    viewer_protocol_policy = "redirect-to-https"

    forwarded_values {
      query_string = false
      cookies {
        forward = "none"
      }
    }
  }

  # SPA 새로고침 시 404 대신 index.html로 — 프론트가 클라이언트 라우팅을 쓰는 걸 전제.
  custom_error_response {
    error_code         = 403
    response_code      = 200
    response_page_path = "/index.html"
  }

  custom_error_response {
    error_code         = 404
    response_code      = 200
    response_page_path = "/index.html"
  }

  restrictions {
    geo_restriction {
      restriction_type = "none"
    }
  }

  viewer_certificate {
    acm_certificate_arn      = data.aws_acm_certificate.cloudfront.arn
    ssl_support_method       = "sni-only"
    minimum_protocol_version = "TLSv1.2_2021"
  }

  tags = {
    Team = "team1"
    Name = "team1-cf-frontend"
  }
}

resource "aws_route53_record" "frontend" {
  zone_id = data.aws_route53_zone.team1.zone_id
  name    = local.frontend_domain
  type    = "A"

  alias {
    name                   = aws_cloudfront_distribution.frontend.domain_name
    zone_id                = aws_cloudfront_distribution.frontend.hosted_zone_id
    evaluate_target_health = false
  }
}

data "aws_iam_policy_document" "frontend_oac" {
  statement {
    actions   = ["s3:GetObject"]
    resources = ["${aws_s3_bucket.frontend.arn}/*"]

    principals {
      type        = "Service"
      identifiers = ["cloudfront.amazonaws.com"]
    }

    condition {
      test     = "StringEquals"
      variable = "AWS:SourceArn"
      values   = [aws_cloudfront_distribution.frontend.arn]
    }
  }
}

resource "aws_s3_bucket_policy" "frontend" {
  bucket = aws_s3_bucket.frontend.id
  policy = data.aws_iam_policy_document.frontend_oac.json
}

output "waf_alb_web_acl_arn" {
  description = "Ingress 어노테이션(alb.ingress.kubernetes.io/wafv2-acl-arn)에서 참조할 WebACL ARN"
  value       = aws_wafv2_web_acl.alb.arn
}

output "acm_alb_certificate_arn" {
  description = "Ingress 어노테이션(alb.ingress.kubernetes.io/certificate-arn)에서 참조"
  value       = data.aws_acm_certificate.alb.arn
}

output "cloudfront_domain_name" {
  value = aws_cloudfront_distribution.frontend.domain_name
}

output "frontend_url" {
  value = "https://${local.frontend_domain}"
}
