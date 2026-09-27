# Infrastructure

단일 Go Lobby & Relay 프로세스를 **호스팅 업체와 무관하게** Ubuntu 24.04 VM 한 대에 배포하기 위한 systemd 설정, 호스트 준비·배포·비용 관리 스크립트를 모은다. 특정 클라우드 API나 계정에 의존하지 않는다. 아래 명령은 저장소 루트에서 실행한다.

```text
infrastructure/
├── ops/             # systemd 유닛·타이머, Caddy 설정과 drop-in, 환경 변수 예시
├── scripts/         # 호스트 준비·패키징·배포·operator 토큰·트래픽 가드와 테스트
└── docs/operations.md
```

| 경로 | 외부 노출 | 수신 위치 |
|---|---|---|
| Player HTTP API | HTTPS 443 (Caddy가 Let's Encrypt 인증서 자동 발급·갱신) | `127.0.0.1:8080` |
| UDP Relay | 공개 UDP 한 포트 | `0.0.0.0:<RELAY_UDP_PORT>` |
| Operator HTTP API | 없음 | `127.0.0.1:8081` |
| SSH | 관리자 IPv4 `/32`만 | `:22` |
| ACME HTTP-01 / HTTPS 리다이렉트 | 80 | Caddy |

호스트에 필요한 것은 Ubuntu 24.04, 고정 공인 IPv4, 해당 IP를 가리키는 DNS A 레코드, 그리고 업체 방화벽(있다면)에서 위 포트를 여는 것뿐이다. 절차는 [운영 가이드](docs/operations.md)를 따른다.

```sh
python3 infrastructure/scripts/tests/traffic-budget.py
python3 infrastructure/scripts/tests/operator-token.py
python3 infrastructure/scripts/tests/deploy-backend.py
python3 infrastructure/scripts/tests/setup-host.py
infrastructure/scripts/package-release.sh /tmp/lobby-relay-release.tar.gz
```

CI(`.github/workflows/ci.yml`)는 모든 push·PR에서 Go 검사와 위 스크립트 테스트를 실행한다. 배포는 관리자 워크스테이션에서 SSH로 수행하며 CI에 서버 자격 증명을 두지 않는다. 특정 업체의 VM·방화벽·DNS를 코드로 관리하려면 이 절차 앞단에 얇은 IaC 계층만 추가하면 된다.

실제 서버 준비와 공개 배포는 아직 실행하지 않았다.
