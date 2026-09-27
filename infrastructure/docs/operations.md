# Lobby Relay 운영 가이드

이 문서는 아직 계정·도메인이 연결되지 않은 배포 절차다. 예시 호스트/인터페이스/금액을 그대로 사용하지 말고 실제 계정에서 확인한 값으로 바꾼다. AWS 최초 부트스트랩과 Terraform 상태 복구는 [인프라 가이드](../terraform/README.md)를 따른다.

## 구성과 권한

서울 Lightsail Ubuntu 24.04 2 GB VM 한 대와 고정 IPv4만 사용한다. 프로세스는 `lobby-relay` 하나이며 `cmd/lobby-relay`의 필수 플래그를 systemd 유닛이 고정한다.

- Player HTTP `127.0.0.1:8080`: Cloudflare WAF → Tunnel(`api_hostname`)로만 도달한다.
- Operator HTTP `127.0.0.1:8081`: 외부에 열지 않는다. Player Token 발급과 room 할당은 VM 내부의 신뢰된 백엔드나 SSH에서 호출한다.
- UDP Relay `0.0.0.0:RELAY_UDP_PORT`: Lightsail 방화벽과 UFW에서 이 포트만 공개한다. grant에는 `RELAY_ADVERTISED_HOST`(DNS-only `relay_hostname`)를 담는다.
- 직접 SSH는 관리자 고정 IPv4 `/32`에만 허용하고 IPv6 인바운드는 닫는다.

GitHub 기본 브랜치와 `infra`, `production` 환경에 승인자·자기 승인 금지·기본 브랜치 배포 제한을 설정하고 기본 브랜치 병합에는 CI 성공을 필수로 요구한다. PR CI에는 배포 자격 증명을 주지 않는다.

| 환경 | 종류 | 정확한 이름과 용도 |
|---|---|---|
| `infra` | Variables | `AWS_ROLE_ARN`: `arn:aws:iam::<실제 계정>:role/lobby-relay-github-infra`; `TF_STATE_BUCKET`: 비공개 상태 버킷; `TF_VARIABLES_JSON`: 아래 비밀 아닌 Terraform 입력 JSON |
| `infra` | Secret | `CLOUDFLARE_API_TOKEN`: 해당 계정/존의 DNS·WAF ruleset·Tunnel·Access app/policy 관리 범위 |
| `production` | Variables | `DEPLOY_HOST`: Access로 보호된 배포 SSH 호스트 |
| `production` | Secrets | `SSH_PRIVATE_KEY`, `SSH_KNOWN_HOSTS`, `TUNNEL_SERVICE_TOKEN_ID`, `TUNNEL_SERVICE_TOKEN_SECRET` |

`TF_VARIABLES_JSON`은 `terraform.tfvars.example`의 키(`aws_account_id`, `cloudflare_account_id`, `cloudflare_zone_id`, `api_hostname`, `relay_hostname`, `relay_udp_port`, `deploy_hostname`, `deploy_access_service_token_id`, `admin_ipv4_cidr`, `ssh_public_key`, `budget_email`, `monthly_budget_usd`, `network_out_alarm_bytes_per_period`)만 사용한다. API 토큰은 넣지 않는다.

Infrastructure workflow를 `apply=false`로 실행해 계획을 검토한다. 같은 커밋에서 `apply=true`와 앞 실행의 `reviewed_plan_sha256`을 입력한다. 새 계획이 달라지면 적용이 중단된다.

## 최초 설치: 직접 관리자 SSH

Tunnel이 아직 없으므로 첫 설치는 관리자 `/32`에서 VM 고정 IPv4로 접속한다.

```sh
sudo cloud-init status --wait
sudo ufw status verbose        # 22/tcp(관리자 /32)와 relay UDP 포트만 허용
sudo ss -lntup
getent passwd lobby-relay cloudflared
stat -c '%U %a %n' /etc/lobby-relay /var/lib/lobby-relay /opt/lobby-relay/releases
ip route show default
```

실제 배포 패키지는 검토·커밋한 깨끗한 체크아웃에서 `go.mod`에 고정된 Go로 만든다. `GO_BINARY`로 실행 파일을 지정할 수 있으며 기본값은 `.tools/go/bin/go`, 다음은 PATH다.

```sh
SHA=$(git rev-parse HEAD)
infrastructure/scripts/package-release.sh /tmp/lobby-relay-release.tar.gz
scp /tmp/lobby-relay-release.tar.gz infrastructure/scripts/deploy-backend.sh infrastructure/scripts/install-runtime-secrets.py \
  infrastructure/scripts/check-traffic-budget.sh infrastructure/scripts/traffic_budget.py "$ADMIN_VM:"
ssh "$ADMIN_VM"
```

VM에서 `sudo -i`로 관리자 셸을 연다. Tunnel 토큰 조회용 API 토큰을 `/root/lobby-relay-bootstrap-api-token`에 root 소유 0600 일반 파일로 전달한다. 토큰 문자열을 명령행·셸 히스토리에 넣지 않는다.

```sh
python3 /home/ubuntu/install-runtime-secrets.py "$CF_ACCOUNT_ID" "$CF_TUNNEL_ID" /root/lobby-relay-bootstrap-api-token
stat -c '%U %a %n' /etc/lobby-relay/operator-token /etc/lobby-relay/tunnel-token
rm -- /root/lobby-relay-bootstrap-api-token
```

`operator-token`은 lobby-relay 0600, `tunnel-token`은 cloudflared 0600이어야 한다. 설치기는 기존 operator 토큰을 보존하며, 없을 때만 `operatorapi.ParseOperatorToken` 형식(32바이트 비영 값의 raw base64url 43자)으로 새로 만든다. operator 토큰은 VM 내부에서 operator API를 호출하는 쪽에만 전달한다.

`/etc/lobby-relay/server.env`(root 0644, 비밀 없음):

```ini
RELAY_ADVERTISED_HOST=relay.example.com
RELAY_UDP_PORT=7777
```

`RELAY_UDP_PORT`는 Terraform `relay_udp_port`와 같아야 한다. `/etc/lobby-relay/traffic.env`(root 0644). `TRAFFIC_INTERFACE`는 default route의 실제 외부 인터페이스로 바꾼다.

```ini
TRAFFIC_INTERFACE=ens5
TRAFFIC_WARN_BYTES=1000000000000
TRAFFIC_STOP_BYTES=2000000000000
```

최초 트래픽 상태를 한 번 초기화한 뒤 배포한다. 기존 상태나 마커가 있으면 `--initialize`는 실패한다.

```sh
install -d -m 0755 /opt/lobby-relay/ops
install -m 0755 /home/ubuntu/check-traffic-budget.sh /home/ubuntu/traffic_budget.py /opt/lobby-relay/ops/
set -a; . /etc/lobby-relay/traffic.env; set +a
/opt/lobby-relay/ops/check-traffic-budget.sh --initialize
bash /home/ubuntu/deploy-backend.sh /home/ubuntu/lobby-relay-release.tar.gz "$SHA"
/opt/lobby-relay/current/scripts/check-deployment.sh health
systemctl status lobby-relay.service cloudflared.service traffic-guard.timer --no-pager
```

## 정기 배포와 장애

Deploy workflow를 기본 브랜치에서 수동 실행한다. Linux 패키지의 모든 파일은 SHA256SUMS로 검사하고 `server.env` 형식을 확인한 뒤 symlink를 바꾸고 서비스를 재시작한다. health는 player·operator HTTP가 인증 없는 요청에 401을 돌려주는지로 확인한다.

**Lobby, Quick Match ticket, Player Token, Relay room은 모두 메모리에만 있으므로 배포·재시작·롤백 때 사라진다.** 현재 서버에는 신규 입장을 막고 활성 room이 비기를 기다리는 drain 기능이 없으므로, 트래픽이 적은 시간에 배포한다. 실패 시 직전 릴리스·unit·가드 파일로 복구하고 다시 확인한다. 첫 배포 실패나 롤백 확인 실패는 직접 SSH 복구가 필요하다. 배포 중 SSH 연결을 운반하는 cloudflared는 재시작하지 않는다.

```sh
sudo /opt/lobby-relay/current/scripts/check-deployment.sh health
sudo journalctl -u lobby-relay.service -u cloudflared.service -u traffic-guard.service --since '1 hour ago'
```

## 비용 중단과 재부팅 복구

가드는 외부 인터페이스 RX+TX를 1분마다 합산한다. UTC 월 경계의 마지막 측정 이후 차이는 새달에 보수적으로 반영한다. 경고는 journal, 중단은 영구 `traffic-stopped` 마커와 **lobby-relay·cloudflared 둘 다** 중단/runtime mask로 처리한다. 공개 UDP는 Tunnel을 거치지 않으므로 Tunnel만 닫아서는 주 트래픽이 멈추지 않는다. 임계치는 항상 `0 < WARN < STOP < 3,000,000,000,000`이다. 공급자 집계 지연 때문에 실제 청구의 절대 상한은 아니다.

재부팅·인터페이스 변경·카운터 감소·상태 손상/유실은 관측하지 못한 전송량이 있으므로 중단한다. 두 서비스의 root `ExecStartPre`가 `--check-start`로 boot ID·카운터·마커를 검사해 시작을 거부한다. Tunnel이 닫히면 GitHub 배포 경로도 닫히므로 직접 관리자 SSH가 필요하다.

수동 긴급 중단:

```sh
install -m 0644 /dev/null /var/lib/lobby-relay/traffic-stopped
sync
systemctl stop lobby-relay.service cloudflared.service
systemctl mask --runtime lobby-relay.service cloudflared.service
```

복구 시 timer를 중단하고 상태를 root 0600으로 백업한 뒤, Lightsail의 이번 UTC 월 NetworkIn/NetworkOut과 VM의 마지막 저장값·현재 카운터를 대조한다. `traffic.json`을 삭제하거나 `--initialize`로 재부팅/월 변경을 우회하지 않는다.

```sh
systemctl stop traffic-guard.timer lobby-relay.service cloudflared.service
cp -p /var/lib/lobby-relay/traffic.json /root/lobby-relay-traffic-before-recovery.json
chmod 0600 /root/lobby-relay-traffic-before-recovery.json
cat /proc/sys/kernel/random/boot_id
cat /var/lib/lobby-relay/traffic.json
```

복구용 상태는 버전 1 JSON의 `month`(현재 UTC YYYY-MM), `boot`(현재 boot ID), `interface`, `rx`/`tx`(현재 sysfs 원시 카운터), `rx_bytes`/`tx_bytes`(검토한 이번 달 누적 상한), `total_bytes`(그 합)를 유지한다. 관리자가 검토한 0600 파일로 원자적으로 교체한다. 회계가 확실하고 합계가 STOP보다 낮으며 비용 재개를 승인한 뒤에만 다음을 수행한다.

```sh
rm -- /var/lib/lobby-relay/traffic-stopped
sync
systemctl unmask --runtime lobby-relay.service cloudflared.service
systemctl reset-failed lobby-relay.service cloudflared.service traffic-guard.service
systemctl start traffic-guard.service
test ! -e /var/lib/lobby-relay/traffic-stopped
systemctl start lobby-relay.service
/opt/lobby-relay/current/scripts/check-deployment.sh health
systemctl start traffic-guard.timer cloudflared.service
```

## 디스크와 connector 유지보수

배포는 릴리스와 `/home/ubuntu/lobby-relay-deploy/<SHA>` 업로드를 자동 삭제하지 않는다. 현재 `readlink -f /opt/lobby-relay/current`와 최근 정상 롤백 대상은 보존하고, 확인한 오래된 SHA 디렉터리만 명시적으로 삭제한다.

cloudflared 업그레이드는 직접 관리자 SSH에서만 수행한다. 검증된 새 바이너리 SHA256과 버전을 확인하고 이전 파일을 보관한 뒤 교체한다. 정기 배포는 이미 설치한 connector를 교체하지 않는다.

## 실제 계정에서 남은 검증

검토한 Terraform apply와 후속 무변경 plan, 상태 lock 경합, 공개 UDP 외 직접 origin 차단(IPv4·IPv6), operator 포트 비노출, Access 허용/거부와 SSH 호스트 키, 롤백·첫 설치, 재부팅 시작 게이트와 비용 마커 복구, 알림 이메일 수신을 확인한다. 단위 테스트·mock·로컬 패키지 검사는 이 live 증거를 대신하지 않는다.
