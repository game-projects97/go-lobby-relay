output "instance_name" { value = aws_lightsail_instance.relay.name }
output "relay_ipv4" { value = aws_lightsail_static_ip.relay.ip_address }
output "relay_endpoint" { value = "${var.relay_hostname}:${var.relay_udp_port}" }
output "tunnel_id" { value = cloudflare_zero_trust_tunnel_cloudflared.relay.id }
output "api_url" { value = "https://${var.api_hostname}" }
