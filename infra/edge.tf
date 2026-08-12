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

  # 접수 API ALB — truss-api.jhyang.click로 오리진 도메인을 잡아야 ALB 리스너의
  # 와일드카드 인증서(*.jhyang.click)로 TLS 검증이 통과한다(ALB의 원 도메인인
  # *.elb.amazonaws.com으로 잡으면 인증서 불일치로 오리진 커넥션이 실패한다).
  origin {
    domain_name = aws_route53_record.api.fqdn
    origin_id   = "team1-orderapi-alb"

    custom_origin_config {
      http_port              = 80
      https_port             = 443
      origin_protocol_policy = "https-only"
      origin_ssl_protocols   = ["TLSv1.2"]
    }
  }

  # 시세 수집기 ALB — truss-collector.jhyang.click로 오리진 도메인을 잡는 이유는
  # 접수 API 오리진과 동일(와일드카드 인증서 검증).
  origin {
    domain_name = aws_route53_record.collector.fqdn
    origin_id   = "team1-collector-alb"

    custom_origin_config {
      http_port              = 80
      https_port             = 443
      origin_protocol_policy = "https-only"
      origin_ssl_protocols   = ["TLSv1.2"]
    }
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

    function_association {
      event_type   = "viewer-request"
      function_arn = aws_cloudfront_function.spa_fallback.arn
    }
  }

  # 프론트가 /order-api/*로 부르는 걸 접수 API ALB로 프록시(실제 fetch 호출 확인함) —
  # 동적 API라 캐시 안 함(CachingDisabled), Idempotency-Key/X-Order-Mode 등 커스텀
  # 헤더를 다 넘겨야 해서 오리진 요청 정책은 AllViewer(전부 전달)를 쓴다.
  ordered_cache_behavior {
    path_pattern           = "/order-api/*"
    allowed_methods        = ["GET", "HEAD", "OPTIONS", "PUT", "POST", "PATCH", "DELETE"]
    cached_methods         = ["GET", "HEAD"]
    target_origin_id       = "team1-orderapi-alb"
    viewer_protocol_policy = "https-only"

    cache_policy_id          = "4135ea2d-6df8-44a3-9df3-4b5a84be39ad" # AWS Managed-CachingDisabled
    origin_request_policy_id = "216adef6-5c7f-47e4-b989-5492eafa07d3" # AWS Managed-AllViewer

    function_association {
      event_type   = "viewer-request"
      function_arn = aws_cloudfront_function.strip_order_api_prefix.arn
    }
  }

  # 프론트가 /v1/collect, /v1/markets/*를 접두사 없이 바로 호출하는 걸 실제
  # 소스(AITraderView.vue, MarketStreamView.vue)로 확인해서 시세 수집기 ALB로
  # 프록시 — backend 자체 라우트가 이미 /v1/...라 경로 재작성이 필요 없다.
  ordered_cache_behavior {
    path_pattern           = "/v1/*"
    allowed_methods        = ["GET", "HEAD", "OPTIONS", "PUT", "POST", "PATCH", "DELETE"]
    cached_methods         = ["GET", "HEAD"]
    target_origin_id       = "team1-collector-alb"
    viewer_protocol_policy = "https-only"

    cache_policy_id          = "4135ea2d-6df8-44a3-9df3-4b5a84be39ad" # AWS Managed-CachingDisabled
    origin_request_policy_id = "216adef6-5c7f-47e4-b989-5492eafa07d3" # AWS Managed-AllViewer
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

# --- 접수 API ALB 연결 (Ingress로 이미 만들어진 ALB를 태그로 조회) ----------
# ALB는 Terraform이 아니라 ALB Controller가 k8s/backend/orderapi-ingress.yaml
# 적용 시 만든다 — 여기서는 그 ALB를 태그(Ingress의 alb.ingress.kubernetes.io/tags
# 어노테이션과 값이 같아야 함)로 찾아서 DNS/CloudFront 배관만 잇는다. 프론트가
# 상대경로 /order-api/*로 호출하므로(frontend/src/views/OrderManagementView.vue 등,
# 실제 소스 확인함) CloudFront가 그 경로를 이 ALB로 프록시해야 한다 — CloudFront
# Function으로 /order-api 접두사를 벗겨서 넘긴다.

data "aws_lb" "orderapi" {
  tags = {
    Name = "team1-alb-orderapi"
  }
}

locals {
  api_domain = "truss-api.${var.domain_name}"
}

resource "aws_route53_record" "api" {
  zone_id = data.aws_route53_zone.team1.zone_id
  name    = local.api_domain
  type    = "A"

  alias {
    name                   = data.aws_lb.orderapi.dns_name
    zone_id                = data.aws_lb.orderapi.zone_id
    evaluate_target_health = true
  }
}

# 시세 수집기(backend) ALB — 프론트가 /order-api 같은 접두사 없이 /v1/collect,
# /v1/markets/*를 그대로 호출하는 걸 실제 소스(AITraderView.vue,
# MarketStreamView.vue)로 확인해서 별도 ALB+도메인으로 노출한다. orderapi와
# 다른 ALB인 이유: 이미 정상 동작 중인 orderapi Ingress를 IngressGroup으로
# 합치면서 건드리는 리스크를 피하려는 것.
data "aws_lb" "collector" {
  tags = {
    Name = "team1-alb-collector"
  }
}

locals {
  collector_domain = "truss-collector.${var.domain_name}"
}

resource "aws_route53_record" "collector" {
  zone_id = data.aws_route53_zone.team1.zone_id
  name    = local.collector_domain
  type    = "A"

  alias {
    name                   = data.aws_lb.collector.dns_name
    zone_id                = data.aws_lb.collector.zone_id
    evaluate_target_health = true
  }
}


# SPA 클라이언트 라우팅 폴백 — custom_error_response(403/404->200 index.html)
# 대신 이걸 쓴다. custom_error_response는 배포 전체에 걸려서 어떤 오리진이
# 낸 404든 다 index.html로 덮어써버리는데, 실제로 /order-api/*의 진짜 404
# 응답(예: DELETE 존재하지 않는 주문)까지 index.html로 뒤집어씌우는 걸 라이브
# 테스트로 확인했다 — API 에러 처리가 통째로 깨지는 문제. 오리진에 요청을
# 보내기 전에 뷰어 요청 단계에서 "정적 파일처럼 안 보이면 index.html로"
# 미리 재작성하는 방식으로 바꿔서, default_cache_behavior(S3)에만 붙이고
# /order-api/* 쪽 ordered_cache_behavior는 이 로직을 아예 안 거치게 한다.
resource "aws_cloudfront_function" "spa_fallback" {
  name    = "team1-spa-fallback"
  runtime = "cloudfront-js-2.0"
  comment = "확장자 없는 경로는 index.html로 (S3 오리진 behavior 전용, API 쪽엔 안 붙임)"
  publish = true
  code    = <<-EOT
    function handler(event) {
      var request = event.request;
      var uri = request.uri;
      var lastSegment = uri.split("/").pop();
      if (!lastSegment.includes(".")) {
        request.uri = "/index.html";
      }
      return request;
    }
  EOT
}

resource "aws_cloudfront_function" "strip_order_api_prefix" {
  name    = "team1-strip-order-api-prefix"
  runtime = "cloudfront-js-2.0"
  comment = "/order-api/xxx -> /xxx (orderapi 자체 라우트엔 /order-api 접두사가 없음)"
  publish = true
  code    = <<-EOT
    function handler(event) {
      var request = event.request;
      request.uri = request.uri.replace(/^\/order-api/, "") || "/";
      return request;
    }
  EOT
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
