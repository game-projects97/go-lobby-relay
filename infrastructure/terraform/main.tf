provider "aws" {
  region              = "ap-northeast-2"
  allowed_account_ids = [var.aws_account_id]
  default_tags { tags = { Project = "lobby-relay", ManagedBy = "terraform" } }
}
provider "cloudflare" {}

resource "aws_lightsail_key_pair" "admin" {
  name       = "lobby-relay-admin"
  public_key = var.ssh_public_key
}
resource "aws_lightsail_instance" "relay" {
  name              = "lobby-relay"
  availability_zone = "ap-northeast-2a"
  blueprint_id      = "ubuntu_24_04"
  bundle_id         = "small_3_0"
  ip_address_type   = "ipv4"
  key_pair_name     = aws_lightsail_key_pair.admin.name
  user_data = templatefile("${path.module}/cloud-init.yaml.tftpl", {
    admin_ipv4_cidr = var.admin_ipv4_cidr
    relay_udp_port  = var.relay_udp_port
  })
}
# UDP relay is the only public data path; Cloudflare Tunnel cannot carry it.
resource "aws_lightsail_instance_public_ports" "relay" {
  instance_name = aws_lightsail_instance.relay.name
  port_info {
    from_port  = 22
    to_port    = 22
    protocol   = "tcp"
    cidrs      = [var.admin_ipv4_cidr]
    ipv6_cidrs = []
    # Explicit: no provider-managed aliases (e.g. lightsail-connect) widen ingress.
    cidr_list_aliases = []
  }
  port_info {
    from_port  = var.relay_udp_port
    to_port    = var.relay_udp_port
    protocol   = "udp"
    cidrs      = ["0.0.0.0/0"]
    ipv6_cidrs = []
    # Explicit: no provider-managed aliases (e.g. lightsail-connect) widen ingress.
    cidr_list_aliases = []
  }
}
resource "aws_lightsail_static_ip" "relay" { name = "lobby-relay" }
resource "aws_lightsail_static_ip_attachment" "relay" {
  static_ip_name = aws_lightsail_static_ip.relay.name
  instance_name  = aws_lightsail_instance.relay.name
}
resource "cloudflare_dns_record" "relay" {
  zone_id = var.cloudflare_zone_id
  name    = var.relay_hostname
  content = aws_lightsail_static_ip.relay.ip_address
  type    = "A"
  proxied = false
  ttl     = 300
}
resource "cloudflare_zero_trust_tunnel_cloudflared" "relay" {
  account_id = var.cloudflare_account_id
  name       = "lobby-relay"
  config_src = "cloudflare"
}
# Operator HTTP (127.0.0.1:8081) is never routed; only the player API and deploy SSH.
resource "cloudflare_zero_trust_tunnel_cloudflared_config" "relay" {
  account_id = var.cloudflare_account_id
  tunnel_id  = cloudflare_zero_trust_tunnel_cloudflared.relay.id
  config = {
    ingress = [
      { hostname = var.api_hostname, service = "http://127.0.0.1:8080" },
      { hostname = var.deploy_hostname, service = "ssh://127.0.0.1:22" },
      { service = "http_status:404" }
    ]
  }
}
resource "cloudflare_dns_record" "api" {
  zone_id = var.cloudflare_zone_id
  name    = var.api_hostname
  content = "${cloudflare_zero_trust_tunnel_cloudflared.relay.id}.cfargotunnel.com"
  type    = "CNAME"
  proxied = true
  ttl     = 1
}
# One resource owns the complete zone phase: import existing rulesets before applying.
resource "cloudflare_ruleset" "api_waf" {
  zone_id = var.cloudflare_zone_id
  name    = "lobby-relay-api-waf"
  kind    = "zone"
  phase   = "http_request_firewall_custom"
  rules = [{
    ref         = "lobby_relay_api_https"
    description = "Reject unencrypted player API requests"
    expression  = "(http.host eq \"${var.api_hostname}\" and not ssl)"
    action      = "block"
    enabled     = true
  }]
}
resource "aws_budgets_budget" "monthly" {
  name         = "lobby-relay-account-monthly"
  budget_type  = "COST"
  limit_amount = tostring(var.monthly_budget_usd)
  limit_unit   = "USD"
  time_unit    = "MONTHLY"
  notification {
    comparison_operator        = "GREATER_THAN"
    threshold                  = 80
    threshold_type             = "PERCENTAGE"
    notification_type          = "ACTUAL"
    subscriber_email_addresses = [var.budget_email]
  }
  notification {
    comparison_operator        = "GREATER_THAN"
    threshold                  = 100
    threshold_type             = "PERCENTAGE"
    notification_type          = "FORECASTED"
    subscriber_email_addresses = [var.budget_email]
  }
}
# AWS provider lacks Lightsail Alarm; its CloudFormation resource supplies it.
# Operator must create and verify the regional Email contact method first.
resource "aws_cloudformation_stack" "alarms" {
  name = "lobby-relay-metric-alarms"
  template_body = jsonencode({
    AWSTemplateFormatVersion = "2010-09-09"
    Resources = {
      for key, config in {
        CPU      = { metric = "CPUUtilization", comparison = "GreaterThanOrEqualToThreshold", threshold = 80 }
        Burst    = { metric = "BurstCapacityPercentage", comparison = "LessThanOrEqualToThreshold", threshold = 20 }
        Outbound = { metric = "NetworkOut", comparison = "GreaterThanOrEqualToThreshold", threshold = var.network_out_alarm_bytes_per_period }
        } : key => {
        Type = "AWS::Lightsail::Alarm"
        Properties = {
          AlarmName             = "lobby-relay-${lower(key)}"
          MonitoredResourceName = aws_lightsail_instance.relay.name
          MetricName            = config.metric
          ComparisonOperator    = config.comparison
          Threshold             = config.threshold
          EvaluationPeriods     = 12
          DatapointsToAlarm     = 1
          NotificationEnabled   = true
          ContactProtocols      = ["Email"]
          NotificationTriggers  = ["ALARM", "INSUFFICIENT_DATA"]
        }
      }
    }
  })
}
resource "cloudflare_zero_trust_access_application" "deploy" {
  account_id           = var.cloudflare_account_id
  name                 = "lobby-relay-deploy-ssh"
  domain               = var.deploy_hostname
  type                 = "self_hosted"
  session_duration     = "1h"
  app_launcher_visible = false
  policies = [{
    name       = "deployment-service-token-only"
    precedence = 1
    decision   = "non_identity"
    include    = [{ service_token = { token_id = var.deploy_access_service_token_id } }]
  }]
}
resource "cloudflare_dns_record" "deploy" {
  zone_id    = var.cloudflare_zone_id
  name       = var.deploy_hostname
  content    = "${cloudflare_zero_trust_tunnel_cloudflared.relay.id}.cfargotunnel.com"
  type       = "CNAME"
  proxied    = true
  ttl        = 1
  depends_on = [cloudflare_zero_trust_access_application.deploy]
}
