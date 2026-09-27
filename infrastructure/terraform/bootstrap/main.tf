terraform {
  required_version = ">= 1.11.0, < 2.0.0"
  required_providers {
    aws = { source = "hashicorp/aws", version = "= 6.63.0" }
  }
  # First apply uses local state. See README before migrating this stack to S3.
}
variable "aws_account_id" { type = string }
variable "state_bucket_name" { type = string }
variable "github_oidc_subject" {
  type        = string
  description = "Exact verified GitHub subject, including immutable IDs if the repository uses them."
  validation {
    condition     = can(regex("^repo:[A-Za-z0-9_.@-]+/[A-Za-z0-9_.@-]+:environment:infra$", var.github_oidc_subject))
    error_message = "Use the exact repository subject ending :environment:infra; wildcards are forbidden."
  }
}
variable "github_oidc_provider_arn" {
  type        = string
  description = "Existing account OIDC provider ARN; bootstrap operator creates/imports it once."
  validation {
    condition     = var.github_oidc_provider_arn == "arn:aws:iam::${var.aws_account_id}:oidc-provider/token.actions.githubusercontent.com"
    error_message = "Supply this account's GitHub Actions OIDC provider ARN."
  }
}
provider "aws" {
  region              = "ap-northeast-2"
  allowed_account_ids = [var.aws_account_id]
}
resource "aws_s3_bucket" "state" {
  bucket        = var.state_bucket_name
  force_destroy = false
  lifecycle { prevent_destroy = true }
}
resource "aws_s3_bucket_public_access_block" "state" {
  bucket                  = aws_s3_bucket.state.id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}
resource "aws_s3_bucket_ownership_controls" "state" {
  bucket = aws_s3_bucket.state.id
  rule { object_ownership = "BucketOwnerEnforced" }
}
resource "aws_s3_bucket_server_side_encryption_configuration" "state" {
  bucket = aws_s3_bucket.state.id
  rule {
    apply_server_side_encryption_by_default { sse_algorithm = "AES256" }
  }
}
resource "aws_s3_bucket_versioning" "state" {
  bucket = aws_s3_bucket.state.id
  versioning_configuration { status = "Enabled" }
}
resource "aws_s3_bucket_policy" "tls" {
  bucket = aws_s3_bucket.state.id
  policy = jsonencode({ Version = "2012-10-17", Statement = [{
    Sid       = "DenyInsecureTransport", Effect = "Deny", Principal = "*", Action = "s3:*",
    Resource  = [aws_s3_bucket.state.arn, "${aws_s3_bucket.state.arn}/*"],
    Condition = { Bool = { "aws:SecureTransport" = "false" } }
  }] })
}
# Protected GitHub environment named exactly "infra"; no PR/ref wildcard trust.
resource "aws_iam_role" "infra" {
  name                 = "lobby-relay-github-infra"
  max_session_duration = 3600
  assume_role_policy = jsonencode({ Version = "2012-10-17", Statement = [{
    Effect = "Allow", Principal = { Federated = var.github_oidc_provider_arn },
    Action = "sts:AssumeRoleWithWebIdentity",
    Condition = { StringEquals = {
      "token.actions.githubusercontent.com:aud" = "sts.amazonaws.com",
      "token.actions.githubusercontent.com:sub" = var.github_oidc_subject
    } }
  }] })
}
resource "aws_iam_role_policy" "state" {
  name = "lobby-relay-state-only"
  role = aws_iam_role.infra.id
  policy = jsonencode({ Version = "2012-10-17", Statement = [
    { Effect = "Allow", Action = ["s3:ListBucket"], Resource = aws_s3_bucket.state.arn,
    Condition = { StringEquals = { "s3:prefix" = ["lobby-relay/production.tfstate", "lobby-relay/production.tfstate.tflock"] } } },
    { Effect = "Allow", Action = ["s3:GetObject", "s3:PutObject"], Resource = "${aws_s3_bucket.state.arn}/lobby-relay/production.tfstate" },
    { Effect = "Allow", Action = ["s3:GetObject", "s3:PutObject", "s3:DeleteObject"], Resource = "${aws_s3_bucket.state.arn}/lobby-relay/production.tfstate.tflock" }
  ] })
}
resource "aws_iam_role_policy" "infrastructure" {
  name = "lobby-relay-infrastructure"
  role = aws_iam_role.infra.id
  # Lightsail CreateInstances/public-ports APIs and discovery lack useful name ARN
  # scoping for this lifecycle; actions are enumerated and restricted to Seoul.
  policy = jsonencode({ Version = "2012-10-17", Statement = [
    { Effect = "Allow", Action = [
      "lightsail:GetInstances", "lightsail:GetInstance", "lightsail:GetInstanceState", "lightsail:GetInstancePortStates",
      "lightsail:GetStaticIp", "lightsail:GetStaticIps", "lightsail:GetKeyPair", "lightsail:GetKeyPairs",
      "lightsail:GetAlarms", "lightsail:GetContactMethods", "lightsail:GetOperation", "lightsail:GetOperations",
      "lightsail:CreateInstances", "lightsail:DeleteInstance", "lightsail:PutInstancePublicPorts",
      "lightsail:AllocateStaticIp", "lightsail:ReleaseStaticIp", "lightsail:AttachStaticIp", "lightsail:DetachStaticIp",
      "lightsail:ImportKeyPair", "lightsail:DeleteKeyPair", "lightsail:TagResource", "lightsail:UntagResource",
      "lightsail:PutAlarm", "lightsail:DeleteAlarm"
    ], Resource = "*", Condition = { StringEquals = { "aws:RequestedRegion" = "ap-northeast-2" } } },
    { Effect = "Allow", Action = ["cloudformation:CreateStack", "cloudformation:UpdateStack", "cloudformation:DeleteStack", "cloudformation:DescribeStacks", "cloudformation:DescribeStackEvents", "cloudformation:GetTemplate", "cloudformation:ListStackResources"], Resource = "arn:aws:cloudformation:ap-northeast-2:${var.aws_account_id}:stack/lobby-relay-metric-alarms/*" },
    { Effect = "Allow", Action = ["budgets:ViewBudget", "budgets:ModifyBudget", "budgets:ListTagsForResource", "budgets:TagResource", "budgets:UntagResource"],
    Resource = "arn:aws:budgets::${var.aws_account_id}:budget/lobby-relay-account-monthly" }
  ] })
}
