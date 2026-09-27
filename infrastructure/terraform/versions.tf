terraform {
  required_version = ">= 1.11.0, < 2.0.0"
  required_providers {
    aws        = { source = "hashicorp/aws", version = "= 6.63.0" }
    cloudflare = { source = "cloudflare/cloudflare", version = "= 5.20.0" }
  }
}
