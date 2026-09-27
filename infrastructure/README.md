# Infrastructure

단일 Go Lobby & Relay 프로세스를 서울 Lightsail VM 한 대에 배포하기 위한 Terraform, systemd 설정, 배포·비용 관리 스크립트를 모은다. 아래 명령은 저장소 루트에서 실행한다.

```text
infrastructure/
├── terraform/       # Lightsail·Cloudflare·비용 알림, S3 상태 부트스트랩
├── ops/             # systemd 서비스·타이머와 환경 변수 예시
├── scripts/         # 패키징·배포·비밀 공급·트래픽 가드와 테스트
└── docs/operations.md
```

| 경로 | 노출 | 수신 위치 |
|---|---|---|
| Player HTTP API | Cloudflare WAF → Tunnel (`api_hostname`) | `127.0.0.1:8080` |
| UDP Relay | 공개 UDP 한 포트 (`relay_hostname`, DNS-only) | `0.0.0.0:<relay_udp_port>` |
| Operator HTTP API | 외부 노출 없음 | `127.0.0.1:8081` |
| 배포 SSH | Cloudflare Access 서비스 토큰 → Tunnel (`deploy_hostname`) | `127.0.0.1:22` |
| 비상 SSH | 관리자 IPv4 `/32` 직접 접속 | `:22` |

- [Terraform 설정과 상태 관리](terraform/README.md)
- [최초 설치·GitHub 설정·배포·복구](docs/operations.md)

GitHub Actions 진입점은 [`.github/workflows/`](../.github/workflows/)에 둔다. `ci.yml`은 모든 push·PR에서 실행하고, `infra.yml`과 `deploy.yml`은 기본 브랜치에서 보호된 환경 승인 후 수동으로만 실행한다.

```sh
terraform -chdir=infrastructure/terraform fmt -check -recursive
terraform -chdir=infrastructure/terraform validate
terraform -chdir=infrastructure/terraform test
python3 infrastructure/scripts/tests/traffic-budget.py
python3 infrastructure/scripts/tests/runtime-secrets.py
python3 infrastructure/scripts/tests/deploy-backend.py
infrastructure/scripts/package-release.sh /tmp/lobby-relay-release.tar.gz
```

실제 클라우드 리소스 생성과 공개 배포는 아직 실행하지 않았다.
