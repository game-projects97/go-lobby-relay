# M1 session handoff

**작성일:** 2026-09-08
**프로젝트:** `go-lobby-relay`
**작업 브랜치:** `codex/m1-relay`
**M1 완료 커밋:** `8b6b8c3e4df559c04659342e0e351aef3bb6d0b8`
**다음 단계:** Phase 6 — 실제 게임 클라이언트가 정해진 뒤 C#/Unity 연동 범위 결정

## 1. 현재 제품 목표

M1은 게임 로직 전체나 Unity Headless를 실행하는 데디케이트 서버가 아니다. 한 Go 프로세스가 다음 기능만 제공하는 초경량 Room/Lobby·Quick Match·UDP Relay 백엔드다.

- 운영자 인증으로 15분 Player Token 발급
- Player Token 기반 public/private Lobby 생성·검색·조회·입장·퇴장·Ready·Start
- 동일 `(queue_key, capacity)` 기준의 1인 FIFO Quick Match
- 매칭 이후 참가자별 비공개 Relay assignment/grant 발급
- 인증된 참가자끼리 같은 Relay room에서 opaque UDP payload 중계
- 단일 바이너리와 인메모리 상태; 재시작 시 모든 token/Lobby/ticket/assignment/Relay state 소멸

M1 실행 경로에는 Redis, database, Kubernetes, Agones, Open Match runtime, Steamworks SDK, FishNet runtime, Unity Editor 또는 Unity build가 없다.

## 2. 이번 세션까지 완료한 이력

| 커밋 | 작업 |
|---|---|
| `cd9e26c` | M1을 Room/Lobby & Quick Match 중심으로 재설계 |
| `b839569` | Phase 4–5 실행 계획 작성 |
| `f54bd8d` | module과 PRD/TRD/roadmap을 `go-lobby-relay` 기준으로 재정렬 |
| `65c85ed` | process nonce 기반 15분 HMAC Player Token 구현 |
| `ab395b3` | single-lock Lobby lifecycle 구현 |
| `7245ffe` | exact-key FIFO Quick Match와 allocation rollback 구현 |
| `93b81a3` | 운영자 Player Token API와 strict player Lobby/Match API 구현 |
| `219b350` | 운영자 HTTP, 플레이어 HTTP, UDP Relay를 한 프로세스로 조립하고 E2E 추가 |
| `8b6b8c3` | Phase 4·5 및 M1 evidence/status 완료 |

기존 Phase 1–3의 Protobuf wire contract, immutable Relay room/session store, UDP handshake/rebind/replay/rate/fan-out 구현은 유지되며 post-match transport로 사용한다.

## 3. 현재 런타임 구조

```text
operator/identity adapter
  └─ management HTTP ─ Player Token 발급 및 기존 Relay room 관리

authenticated player clients
  └─ player HTTP ─ Lobby lifecycle / Quick Match / 자기 assignment 조회

matched player clients
  └─ authenticated UDP ─ bind/rebind / same-room payload relay

single Go process
  ├─ playerauth.Auth
  ├─ lobby.Manager
  ├─ store.Store
  ├─ management HTTP listener
  ├─ player HTTP listener
  ├─ UDP Relay listener
  └─ shared expiry sweeper
```

핵심 패키지:

- `internal/playerauth`: Player Token issue/verify
- `internal/lobby`: Lobby와 FIFO ticket/match 상태
- `internal/playerapi`: Player Bearer 인증과 Lobby/Match HTTP API
- `internal/control`: operator 인증, Player Token과 Relay room control API
- `internal/store`: immutable Relay room/grant/binding authority
- `internal/relay`: synchronous bounded UDP adapter
- `internal/server`: 세 listener와 lifecycle 조립
- `cmd/relay`: strict CLI와 token-file 검증

## 4. M1 검증 결과

검증된 source candidate는 `219b35037126d7aba22ad935c87c2be8b167509f`이고 evidence/status closure는 `8b6b8c3`이다.

- M1 요구사항: 23/23 Complete
- 프로젝트 전체 요구사항: 23 Complete / 14 Pending
- Phase: 1–5 Complete, Phase 6–9 Pending
- `make protocol-check`: PASS
- `make go-test`: PASS
- Player Token/Lobby/Quick Match/control/player API/server/Relay uncached tests: PASS
- 동일 패키지 race detector 및 실제 CLI subprocess: PASS
- protocol decoder와 Relay dispatch fuzz 각 10초: PASS
- fresh-cache `go vet ./...`: PASS
- `make relay-build`, `out/relay` executable: PASS
- 실제 두 HTTP·UDP client로 Lobby 경로와 Quick Match 경로 모두 payload exchange: PASS

상세 근거:

- `docs/evidence/m1/phase-4.md`
- `docs/evidence/m1/phase-5.md`
- `docs/evidence/m1/milestone-1.md`

## 5. 다음 세션에서 해야 할 일

M1은 완료됐다. 다음 작업은 M2의 Phase 6이며, 코드를 바로 추가하기 전에 실제 게임 클라이언트 선택부터 확정해야 한다.

1. 실제 연동할 게임 클라이언트 저장소/구조와 네트워크 책임을 확인한다.
2. Unity를 사용한다면 그때 Editor 버전, desktop/mobile target, Mono/IL2CPP와 실기기 matrix를 결정한다.
3. C# client가 Player Token 발급 이후 Lobby/Quick Match API와 UDP handshake를 어떻게 호출할지 brainstorming한다.
4. FishNet은 실제 gameplay transport 책임과 현재 Relay 책임이 겹치는지 확인한 뒤 필요할 때만 adapter로 채택한다.
5. Steamworks는 실제 Steam identity 검증이 필요할 때 Player Token 발급 앞단의 identity adapter로만 검토한다.
6. 승인된 설계 이후 Phase 6 실행 계획을 작성하고 TDD로 구현한다.

Phase 6에서 피해야 할 일:

- 실제 클라이언트가 없는데 특정 Unity patch 버전을 먼저 고정하지 않는다.
- Steamworks/FishNet/Open Match, Redis/Kubernetes를 미래 가능성만으로 추가하지 않는다.
- M1의 Lobby와 immutable Relay room을 하나의 mutable 객체로 합치지 않는다.
- Player API body/path의 `player_id`를 인증 주체로 신뢰하지 않는다.

## 6. 현재 로컬 작업 트리 주의사항

이 인계 문서를 만들기 전부터 아래 사용자 변경이 존재했다. 이번 인계 커밋에는 포함하지 않았으며 다음 세션에서도 자동으로 삭제·복원·커밋하면 안 된다.

- modified: `docs/PRD.md`
- modified: `docs/TRD.md`
- untracked: `docs/PRD 2.md`
- untracked: `docs/TRD 2.md`

현재 modified 문서는 M1 완료 v5 문서를 과거 Unity-first v4 내용으로 크게 교체하는 형태이며, `* 2.md` 파일은 M1 완료 문서 사본으로 보인다. 어느 버전을 authoritative로 삼을지 사용자 확인 전까지 그대로 보존한다. 원격에 push된 M1 완료 기준은 커밋 `8b6b8c3`의 문서다.

## 7. 다음 세션 시작 명령

새 clone에서 시작할 경우:

```bash
git clone https://github.com/game-projects97/go-lobby-relay.git
cd go-lobby-relay
git switch -c codex/phase-6-client-integration
git log -10 --oneline
make go-test
```

현재 로컬 workspace를 이어 사용할 경우:

```bash
cd /Users/igyeongseob/Documents/사이드프로젝트
git status --short --branch
git log -10 --oneline
```

현재 workspace에는 위 6절의 사용자 문서 변경이 남아 있으므로 새 branch 전환 전에 반드시 보존 상태를 확인한다.

다음 Codex 세션에 전달할 권장 프롬프트:

```text
go-lobby-relay의 docs/handoffs/2026-09-08-m1-session-handoff.md와
docs/evidence/m1/milestone-1.md를 먼저 읽고 현재 git status를 확인해줘.
M1은 완료됐으므로 기존 Lobby/Relay 책임을 변경하지 말고,
실제 게임 클라이언트 정보를 기준으로 Phase 6 C#/Unity 연동을 brainstorming부터 진행해줘.
현재 로컬에 PRD/TRD 미커밋 변경이 있으면 사용자 작업으로 보존하고 임의로 포함하거나 되돌리지 마.
```
