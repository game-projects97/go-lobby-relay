# Lobby relay infrastructure

This stack describes one Ubuntu 24.04 Lightsail VM in Seoul (`ap-northeast-2a`, `small_3_0`, 2 GB), one static IPv4, and no autoscaling. Confirm the blueprint/bundle availability, RAM, regional price and transfer allowance with the actual account before approving a plan. No cloud resources were applied during implementation.

Terraform owns the player API, relay and deploy DNS records, the remotely managed Tunnel and ingress, the API WAF rule, the Access application, the VM and its firewall, the account budget and metric alarms.

## Account bootstrap

Use a dedicated AWS account if possible. The bootstrap operator uses a reviewed administrator/SSO role locally; this is a one-time prerequisite, not a long-lived CI key. Supply verified AWS/Cloudflare accounts, zone, owned hostnames, SSH public key, admin IPv4 `/32`, email and cost thresholds. Examples are placeholders and are deliberately unusable as production configuration.

1. Create or identify the account's GitHub OIDC provider (`https://token.actions.githubusercontent.com`, audience `sts.amazonaws.com`). Get its ARN. If Lightsail has never been used, have the operator establish the Lightsail service-linked role; CI cannot create IAM roles.
2. Protect GitHub environments named exactly `infra` and `production`: required reviewers, prevent self-review, deployment branch restricted to the protected main branch; do not permit pull-request jobs to request credentials. Confirm the actual repository OIDC `sub`. New GitHub repositories can include immutable owner/repository IDs. Set `github_oidc_subject` to that exact subject ending `:environment:infra`, without wildcards.
3. Copy `bootstrap/terraform.tfvars.example` to ignored `bootstrap/terraform.tfvars`. Run `umask 077`, then the bootstrap commands below. Review the plan before applying: this creates the S3 bucket and infra role, never an application VM.

```sh
terraform -chdir=infrastructure/terraform/bootstrap init
terraform -chdir=infrastructure/terraform/bootstrap plan -out=bootstrap.tfplan
terraform -chdir=infrastructure/terraform/bootstrap apply bootstrap.tfplan
```

4. Keep the initial local `terraform.tfstate` and backup in an encrypted operator-controlled backup location with mode 0600. Record the state bucket name and infra role ARN. Copy `bootstrap/backend.tf.example` to `bootstrap/backend.tf`; copy `backend.tfbackend.example` to ignored `backend.tfbackend` and fill in the bucket. Run:

```sh
terraform -chdir=infrastructure/terraform/bootstrap init -migrate-state -backend-config=../backend.tfbackend
terraform -chdir=infrastructure/terraform/bootstrap state list
aws s3api head-object --bucket "$LOBBY_RELAY_STATE_BUCKET" --key lobby-relay/bootstrap.tfstate
```

Confirm the remote state and version exist before removing local working copies; retain the encrypted backup. Bootstrap uses `lobby-relay/bootstrap.tfstate`; application uses `lobby-relay/production.tfstate`. The CI role cannot read/write bootstrap state or modify IAM/S3 policy. The S3 bucket has public-access blocks, ownership enforcement, SSE-S3, versioning, TLS-only policy, and `prevent_destroy`; no lifecycle expiry erases old state versions.

5. Configure the regional Lightsail Email contact once and verify its confirmation email. The current AWS provider has no contact-method resource.

```sh
aws lightsail get-contact-methods --region ap-northeast-2
aws lightsail create-contact-method --region ap-northeast-2 --protocol Email --contact-endpoint "$LOBBY_RELAY_BUDGET_EMAIL"
```

6. Create a Cloudflare Access service token outside Terraform. Store its client ID/secret in protected `production` environment secrets; put only its UUID in `deploy_access_service_token_id`. This UUID differs from its client ID. Infrastructure needs a scoped Cloudflare API token for the selected zone DNS/ruleset edits, account Tunnel and Access app/policy. Routine production deployment needs only Access service-token credentials and an SSH key with verified host keys. The initial administrator uses a separate scoped API token for Tunnel-token retrieval; do not put that bootstrap token in the production environment.

## Plan and provision

Copy `terraform.tfvars.example` to ignored `terraform.tfvars`, replacing every placeholder. Set `CLOUDFLARE_API_TOKEN` through the credential store/environment; AWS uses SSO locally or the bootstrap output role via GitHub OIDC.

Before creating the zone ruleset, inventory the `http_request_firewall_custom` phase. The resource owns the entire zone phase; existing rules must be imported and preserved, not overwritten:

```sh
terraform -chdir=infrastructure/terraform init -backend-config=backend.tfbackend
terraform -chdir=infrastructure/terraform import cloudflare_ruleset.api_waf 'ZONE_ID/RULESET_ID'
terraform -chdir=infrastructure/terraform plan -out=production.tfplan
```

Review that plan for exactly one VM, fixed bundle, private/versioned state, admin `/32` SSH plus the single relay UDP port only, no IPv6 ingress, no public HTTP or operator port, and no unexpected ruleset deletion. Apply only the reviewed saved plan. Mocked tests cannot prove account permissions or protection effectiveness.

The role grants explicit Lightsail lifecycle actions restricted to Seoul, named CloudFormation stack actions, the named budget, and the exact production state/lock objects. Some Lightsail actions use `Resource="*"` because creation/discovery cannot all be expressed by a fixed name ARN. Review with IAM Access Analyzer and CloudTrail, then tighten where supported.

## Origin and deployment contract

Public Lightsail ingress is SSH from the administrator IPv4 `/32` and UDP on `relay_udp_port` (default 7777) from any IPv4. The relay listens on `udp4`, so no IPv6 ingress is opened. UFW mirrors the same two rules and denies everything else. `relay_hostname` is a **DNS-only** A record to the static IP: Cloudflare's proxy cannot carry arbitrary UDP, and players need the origin address to reach the relay. The origin IP is therefore public; protection of the UDP path relies on the relay's own grant authentication, replay protection and hard limits, not on Cloudflare.

Tunnel routes are player API → `http://127.0.0.1:8080`, deploy → `ssh://127.0.0.1:22`, then `http_status:404`. The operator API on `127.0.0.1:8081` is never routed; issue player tokens and allocate rooms from a trusted backend on the VM or over SSH. Access on the deploy hostname admits only the selected service token (`non_identity`); SSH public-key authentication is still required. Initial bootstrap uses direct admin SSH, since the Tunnel cannot carry deployment until cloudflared is installed.

cloud-init creates non-login users `lobby-relay` and `cloudflared`, `/opt/lobby-relay/releases`, root-owned `/etc/lobby-relay` mode 0711 and `/var/lib/lobby-relay` owned by root (prevents services deleting traffic safety state). Install secrets mode 0600 owned by their service user: `operator-token` for lobby-relay, `tunnel-token` for cloudflared. `server.env` contains only nonsecret settings. Never inject credentials through Terraform variables/user-data or `remote-exec`.

## Cost and health

Account-wide monthly budget sends actual 80% and forecast 100% email warnings. Budgets are delayed notifications, not a payment cutoff. The CloudFormation stack configures native Lightsail CPU >=80%, burst capacity <=20%, and NetworkOut >= configured bytes **per five-minute data point**. NetworkOut is a warning, not a cumulative monthly transfer cap.

Memory is sampled every minute into bounded journald by `lobby-relay-memory-report.timer`; values >=85% include a WARNING. Journald is limited to 100 MB persistent/32 MB runtime, seven days. The host traffic guard in `infrastructure/scripts/` stops **both** the relay and the Tunnel when the monthly limit is reached, because public UDP bypasses the Tunnel.

## Offline checks

```sh
terraform -chdir=infrastructure/terraform/bootstrap init -backend=false -input=false
terraform -chdir=infrastructure/terraform/bootstrap validate
terraform -chdir=infrastructure/terraform/bootstrap test
terraform -chdir=infrastructure/terraform init -backend=false -input=false
terraform -chdir=infrastructure/terraform validate
terraform -chdir=infrastructure/terraform test
```

Commit both `.terraform.lock.hcl` files. Refresh deliberately with `terraform providers lock -platform=darwin_arm64 -platform=linux_amd64` in each stack; never commit `.terraform`, state, saved plans or populated variable/backend files.

Destroy only the application stack after checking the plan; the bootstrap state bucket remains. Destroying the VM or rolling back a binary does not restore in-memory lobbies, tickets or relay rooms. See [operations](../docs/operations.md).
