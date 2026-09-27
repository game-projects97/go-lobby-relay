mock_provider "aws" {}
variables {
  aws_account_id           = "123456789012"
  state_bucket_name        = "lobby-relay-test-state"
  github_oidc_subject      = "repo:example/lobby-relay:environment:infra"
  github_oidc_provider_arn = "arn:aws:iam::123456789012:oidc-provider/token.actions.githubusercontent.com"
}
run "private_versioned_state" {
  command = plan
  assert {
    condition     = aws_s3_bucket_public_access_block.state.block_public_acls && aws_s3_bucket_public_access_block.state.block_public_policy && aws_s3_bucket_public_access_block.state.ignore_public_acls && aws_s3_bucket_public_access_block.state.restrict_public_buckets
    error_message = "All S3 public access blocks must remain enabled."
  }
  assert {
    condition     = one(aws_s3_bucket_versioning.state.versioning_configuration).status == "Enabled"
    error_message = "State requires recoverable versions."
  }
  assert {
    condition     = jsondecode(aws_iam_role.infra.assume_role_policy).Statement[0].Condition.StringEquals["token.actions.githubusercontent.com:sub"] == "repo:example/lobby-relay:environment:infra"
    error_message = "OIDC trust must target the protected infra environment."
  }
}
