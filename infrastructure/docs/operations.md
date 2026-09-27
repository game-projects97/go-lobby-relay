# Lobby Relay 운영 가이드

호스팅 업체와 무관한 단일 VM 배포 절차다. 예시 호스트·주소·금액을 그대로 사용하지 말고 실제 값으로 바꾼다.

## 구성

프로세스는 `lobby-relay` 하나이며 `cmd/lobby-relay`의 필수 플래그를 systemd 유닛이 고정한다. TLS는 Ubuntu 패키지의 Caddy가 맡는다.

- **Player HTTP** `127.0.0.1:8080`: Caddy가 `API_HOSTNAME`의 443에서 TLS를 종료하고 이 주소로만 전달한다. 앱은 `https://API_HOSTNAME/v1/...`로 접속한다. player API 응답에는 `grant_secret`이 들어 있으므로 평문 HTTP로 공개하지 않는다.
- **Operator HTTP** `127.0.0.1:8081`: 외부에 열지 않고 Caddy도 전달하지 않는다. Player Token 발급과 room 할당은 같은 호스트의 신뢰된 백엔드나 SSH에서 호출한다.
- **UDP Relay** `0.0.0.0:RELAY_UDP_PORT`: 공개한다. grant에는 `RELAY_ADVERTISED_HOST`를 담는다. 서버가 한 대이므로 `API_HOSTNAME`과 같은 이름을 써도 된다.
- **SSH**: 관리자 고정 IPv4 `/32`에서만 허용하고, 비밀번호 로그인은 끈다.

## 준비물

1. Ubuntu 24.04 VM 한 대와 고정 공인 IPv4. 업체 제한은 없다.
2. SSH 공개키로 로그인되는 관리자 계정.
3. `API_HOSTNAME`(필요하면 `RELAY_ADVERTISED_HOST`도)의 A 레코드가 VM IP를 가리킬 것. DNS 업체 제한은 없다. Caddy가 인증서를 받으려면 배포 전에 DNS가 전파되어 있어야 한다.
4. 업체 방화벽(보안 그룹 등)이 있다면 22/tcp(관리자 `/32`), 80/tcp, 443/tcp, `RELAY_UDP_PORT`/udp만 연다. VM 안의 UFW도 `setup-host.sh`가 같은 규칙으로 설정한다.
5. 업체의 월 전송량 한도와 초과 요금을 확인해 트래픽 가드 임계치를 정한다.

## 최초 설치

관리 워크스테이션의 검토·커밋한 깨끗한 체크아웃에서 `go.mod`에 고정된 Go로 패키지를 만든다. `GO_BINARY`로 실행 파일을 지정할 수 있으며, 기본값은 `.tools/go/bin/go`이고 없으면 PATH의 `go`를 쓴다.

```sh
SHA=$(git rev-parse HEAD)
infrastructure/scripts/package-release.sh /tmp/lobby-relay-release.tar.gz
scp /tmp/lobby-relay-release.tar.gz "$ADMIN_VM:"
ssh "$ADMIN_VM"
```

VM에서 패키지를 풀고 호스트를 준비한다. 이 스크립트는 다음을 수행한다.

- 패키지(ufw, caddy, python3) 설치
- `lobby-relay` 시스템 사용자와 디렉터리 생성
- SSH 강화, journald 용량 제한
- `server.env`, `traffic.env` 작성
- UFW 설정, operator 토큰 생성, 트래픽 장부 초기화

공개 서비스는 시작하지 않는다. 다시 실행해도 되며, 기존 operator 토큰과 트래픽 장부는 보존한다.

```sh
mkdir -p ~/lobby-relay-release && tar -xzf ~/lobby-relay-release.tar.gz -C ~/lobby-relay-release
sudo --preserve-env=SSH_CONNECTION ~/lobby-relay-release/scripts/setup-host.sh \
  --api-hostname api.example.com \
  --relay-udp-port 7777 \
  --admin-ssh-cidr 203.0.113.4/32 \
  --traffic-warn-bytes 1000000000000 \
  --traffic-stop-bytes 2000000000000
```

- `--relay-hostname`을 생략하면 `--api-hostname`과 같은 값을 쓴다.
- `--traffic-interface`를 생략하면 기본 경로의 인터페이스를 쓴다.
- 현재 SSH 접속 주소가 `--admin-ssh-cidr`와 다르면 접속이 끊기지 않도록 중단한다. `SSH_CONNECTION`을 넘기지 않으면 이 검사를 할 수 없으니 주소를 직접 확인한다.
- SSH 공개키가 하나도 없으면 비밀번호 로그인을 끄기 전에 중단한다.

첫 배포:

```sh
sudo bash ~/lobby-relay-release/scripts/deploy-backend.sh ~/lobby-relay-release.tar.gz "$SHA"
sudo /opt/lobby-relay/current/scripts/check-deployment.sh health
systemctl status lobby-relay.service caddy.service traffic-guard.timer --no-pager
curl -sS -o /dev/null -w '%{http_code}\n' https://api.example.com/v1/lobbies   # 401이면 정상
```

operator 토큰은 `/etc/lobby-relay/operator-token`(lobby-relay 0600)에 있다. 형식은 `operatorapi.ParseOperatorToken`과 같다(0이 아닌 32바이트의 raw base64url 43자). 이 토큰은 VM 내부에서 operator API를 호출하는 쪽에만 전달한다.

## 정기 배포와 장애

워크스테이션에서 새 패키지를 만들어 `scp`로 올리고, `deploy-backend.sh`를 다시 실행한다. 배포 스크립트는 다음 순서로 진행한다.

1. 모든 파일을 SHA256SUMS로 검사하고 `server.env` 형식을 확인한다.
2. symlink, systemd 유닛, Caddy 설정과 drop-in을 교체하고 relay를 재시작한다.
3. health를 확인한다. player·operator HTTP가 인증 없는 요청에 401을 돌려주면 정상이다.
4. Caddy는 reload로 새 설정을 반영한다. TLS 연결은 끊지 않는다.

**Lobby, Quick Match ticket, Player Token, Relay room은 모두 메모리에만 있으므로 배포·재시작·롤백 때 사라진다.** 현재 서버에는 drain 기능이 없으므로 트래픽이 적은 시간에 배포한다.

실패하면 직전 릴리스의 유닛·Caddy 설정·가드 파일로 복구하고 다시 확인한다. 첫 배포가 실패하면 relay를 중단한다.

```sh
sudo /opt/lobby-relay/current/scripts/check-deployment.sh health
sudo journalctl -u lobby-relay.service -u caddy.service -u traffic-guard.service --since '1 hour ago'
```

## 비용 중단과 재부팅 복구

가드는 외부 인터페이스의 RX+TX를 1분마다 합산한다. UTC 월 경계에서 마지막 측정 이후의 차이는 새달에 보수적으로 반영한다.

- **경고:** journal에 기록한다.
- **중단:** 영구 `traffic-stopped` 마커를 남기고 **lobby-relay·caddy 둘 다** 중단하고 runtime mask한다. 공개 경로는 UDP와 HTTPS 두 가지뿐이다.
- **임계치 조건:** 항상 `0 < WARN < STOP < 3,000,000,000,000`이어야 한다.
- 업체의 집계 지연 때문에 실제 청구 금액의 절대 상한은 아니다. 업체가 제공하는 예산·사용량 알림도 함께 켠다.

재부팅, 인터페이스 변경, 카운터 감소, 상태 파일 손상·유실이 생기면 관측하지 못한 전송량이 있을 수 있으므로 중단한다. 두 서비스는 root `ExecStartPre`의 `--check-start`로 boot ID·카운터·마커를 검사하고, 이상이 있으면 시작을 거부한다. Caddy는 drop-in(`caddy.service.d/lobby-relay.conf`)으로 같은 검사를 받는다.

수동 긴급 중단:

```sh
install -m 0644 /dev/null /var/lib/lobby-relay/traffic-stopped
sync
systemctl stop lobby-relay.service caddy.service
systemctl mask --runtime lobby-relay.service caddy.service
```

복구 순서:

1. timer를 중단하고 상태 파일을 root 0600으로 백업한다.
2. 업체 콘솔의 이번 UTC 월 전송량과 VM의 마지막 저장값·현재 카운터를 대조한다.
3. `traffic.json`을 삭제하거나 `--initialize`로 재부팅·월 변경을 우회하지 않는다.

```sh
systemctl stop traffic-guard.timer lobby-relay.service caddy.service
cp -p /var/lib/lobby-relay/traffic.json /root/lobby-relay-traffic-before-recovery.json
chmod 0600 /root/lobby-relay-traffic-before-recovery.json
cat /proc/sys/kernel/random/boot_id
cat /var/lib/lobby-relay/traffic.json
```

복구용 상태는 버전 1 JSON이며 다음 필드를 유지한다.

| 필드 | 값 |
|---|---|
| `month` | 현재 UTC `YYYY-MM` |
| `boot` | 현재 boot ID |
| `interface` | 외부 인터페이스 이름 |
| `rx`, `tx` | 현재 sysfs 원시 카운터 |
| `rx_bytes`, `tx_bytes` | 검토한 이번 달 누적 상한 |
| `total_bytes` | `rx_bytes`와 `tx_bytes`의 합 |

관리자가 검토한 0600 파일로 원자적으로 교체한다. 다음 명령은 세 조건을 모두 만족할 때만 실행한다. 회계가 확실하고, 합계가 STOP보다 낮고, 비용 재개를 승인했을 때다.

```sh
rm -- /var/lib/lobby-relay/traffic-stopped
sync
systemctl unmask --runtime lobby-relay.service caddy.service
systemctl reset-failed lobby-relay.service caddy.service traffic-guard.service
systemctl start traffic-guard.service
test ! -e /var/lib/lobby-relay/traffic-stopped
systemctl start lobby-relay.service
/opt/lobby-relay/current/scripts/check-deployment.sh health
systemctl start traffic-guard.timer caddy.service
```

## 유지보수

- **디스크:** 배포는 `/opt/lobby-relay/releases/<SHA>`를 자동 삭제하지 않는다. 현재 `readlink -f /opt/lobby-relay/current`와 최근 정상 롤백 대상은 보존하고, 확인한 오래된 SHA 디렉터리만 명시적으로 삭제한다.
- **패키지 업데이트:** Caddy와 OS 패키지는 `unattended-upgrades`로 보안 업데이트를 받는다.
- **인증서:** Caddy가 `/var/lib/caddy`에 저장하고 자동 갱신한다.

## 실제 서버에서 남은 검증

- 인증서 발급과 갱신
- 업체 방화벽과 UFW에서 공개 포트(22/80/443/UDP) 외 차단(IPv4·IPv6)
- operator 포트가 외부에 노출되지 않는지
- 롤백과 첫 설치
- 재부팅 시작 게이트와 비용 마커 복구
- 업체 예산 알림 수신

단위 테스트와 로컬 검사는 이 실서버 증거를 대신하지 않는다.
