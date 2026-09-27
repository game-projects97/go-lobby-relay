mock_provider "aws" {}
mock_provider "cloudflare" {}
variables {
  aws_account_id                 = "123456789012"
  cloudflare_account_id          = "11111111111111111111111111111111"
  cloudflare_zone_id             = "22222222222222222222222222222222"
  api_hostname                   = "api.example.com"
  relay_hostname                 = "relay.example.com"
  deploy_hostname                = "deploy.example.com"
  deploy_access_service_token_id = "11111111-1111-1111-1111-111111111111"
  admin_ipv4_cidr                = "192.0.2.1/32"
  ssh_public_key                 = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAITestOnly"
  budget_email                   = "operator@example.com"
  monthly_budget_usd             = 25
}
run "fixed_capacity_and_minimal_origin" {
  command = plan
  assert {
    condition     = aws_lightsail_instance.relay.bundle_id == "small_3_0" && aws_lightsail_instance.relay.ip_address_type == "ipv4" && aws_lightsail_instance.relay.availability_zone == "ap-northeast-2a"
    error_message = "Instance must remain the fixed Seoul 2 GB IPv4 bundle."
  }
  assert {
    condition = length(aws_lightsail_instance_public_ports.relay.port_info) == 2 && alltrue([
      for rule in aws_lightsail_instance_public_ports.relay.port_info : length(rule.ipv6_cidrs) == 0 && (
        (rule.protocol == "tcp" && rule.from_port == 22 && rule.to_port == 22 && toset(rule.cidrs) == toset(["192.0.2.1/32"])) ||
        (rule.protocol == "udp" && rule.from_port == 7777 && rule.to_port == 7777)
      )
    ])
    error_message = "Only admin /32 SSH and the single relay UDP port may be reachable at the origin."
  }
  assert {
    condition     = !cloudflare_dns_record.relay.proxied && cloudflare_dns_record.relay.type == "A"
    error_message = "Relay UDP hostname must be DNS-only; Cloudflare proxy cannot carry UDP."
  }
  assert {
    condition     = cloudflare_zero_trust_tunnel_cloudflared_config.relay.config.ingress[0].service == "http://127.0.0.1:8080" && cloudflare_zero_trust_tunnel_cloudflared_config.relay.config.ingress[1].service == "ssh://127.0.0.1:22" && cloudflare_zero_trust_tunnel_cloudflared_config.relay.config.ingress[2].service == "http_status:404" && length(cloudflare_zero_trust_tunnel_cloudflared_config.relay.config.ingress) == 3
    error_message = "Tunnel ingress must target player API/SSH loopback only, never operator HTTP, with a deny catchall."
  }
  assert {
    condition     = length(cloudflare_zero_trust_access_application.deploy.policies) == 1 && cloudflare_zero_trust_access_application.deploy.policies[0].decision == "non_identity" && length(cloudflare_zero_trust_access_application.deploy.policies[0].include) == 1
    error_message = "Deploy Access must allow only its explicit service token."
  }
}
run "reject_wide_ssh" {
  command = plan
  variables { admin_ipv4_cidr = "0.0.0.0/0" }
  expect_failures = [var.admin_ipv4_cidr]
}
run "reject_privileged_relay_port" {
  command = plan
  variables { relay_udp_port = 53 }
  expect_failures = [var.relay_udp_port]
}
