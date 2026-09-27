terraform {
  backend "s3" {
    key          = "lobby-relay/production.tfstate"
    region       = "ap-northeast-2"
    encrypt      = true
    use_lockfile = true
  }
}
