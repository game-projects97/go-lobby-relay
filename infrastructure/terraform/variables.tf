variable "aws_account_id" {
  type = string
  validation {
    condition     = can(regex("^[0-9]{12}$", var.aws_account_id))
    error_message = "Supply the verified 12-digit AWS account ID."
  }
}
variable "cloudflare_account_id" { type = string }
variable "cloudflare_zone_id" { type = string }
variable "api_hostname" {
  type        = string
  description = "Player HTTP API hostname, served through the Cloudflare Tunnel."
  validation {
    condition     = can(regex("^[a-z0-9][a-z0-9.-]+[a-z0-9]$", var.api_hostname))
    error_message = "Use a lowercase DNS hostname without scheme, path, or wildcard."
  }
}
variable "relay_hostname" {
  type        = string
  description = "DNS-only UDP relay hostname advertised in player grants."
  validation {
    condition     = can(regex("^[a-z0-9][a-z0-9.-]+[a-z0-9]$", var.relay_hostname)) && var.relay_hostname != var.api_hostname
    error_message = "Relay must be a separate lowercase DNS hostname."
  }
}
variable "relay_udp_port" {
  type    = number
  default = 7777
  validation {
    condition     = var.relay_udp_port >= 1024 && var.relay_udp_port <= 65535 && floor(var.relay_udp_port) == var.relay_udp_port
    error_message = "Relay UDP port must be an unprivileged port number."
  }
}
variable "admin_ipv4_cidr" {
  type = string
  validation {
    condition     = can(cidrnetmask(var.admin_ipv4_cidr)) && endswith(var.admin_ipv4_cidr, "/32")
    error_message = "SSH requires one administrator IPv4 /32, used for emergency direct access."
  }
}
variable "ssh_public_key" {
  type = string
  validation {
    condition     = can(regex("^ssh-(ed25519|rsa) [A-Za-z0-9+/=]+( .*)?$", var.ssh_public_key))
    error_message = "Supply an SSH public key, never a private key."
  }
}
variable "budget_email" {
  type = string
  validation {
    condition     = can(regex("^[^@ ]+@[^@ ]+\\.[^@ ]+$", var.budget_email))
    error_message = "Supply the operator's budget/metric alert email."
  }
}
variable "monthly_budget_usd" {
  type = number
  validation {
    condition     = var.monthly_budget_usd > 0
    error_message = "Monthly budget must be positive."
  }
}
variable "network_out_alarm_bytes_per_period" {
  type    = number
  default = 1000000000
  validation {
    condition     = var.network_out_alarm_bytes_per_period > 0
    error_message = "Network warning threshold must be positive."
  }
}

variable "deploy_hostname" {
  type = string
  validation {
    condition     = can(regex("^[a-z0-9][a-z0-9.-]+[a-z0-9]$", var.deploy_hostname)) && !contains([var.api_hostname, var.relay_hostname], var.deploy_hostname)
    error_message = "Deploy must be a separate lowercase DNS hostname."
  }
}
variable "deploy_access_service_token_id" {
  type        = string
  description = "Existing Access service token UUID (not Client ID or secret); create outside Terraform."
  validation {
    condition     = can(regex("^[0-9a-f-]{36}$", var.deploy_access_service_token_id))
    error_message = "Supply the existing Access service token UUID."
  }
}
