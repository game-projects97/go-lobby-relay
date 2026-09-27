output "state_bucket_name" { value = aws_s3_bucket.state.id }
output "infra_role_arn" { value = aws_iam_role.infra.arn }
output "bootstrap_state_key" { value = "lobby-relay/bootstrap.tfstate" }
