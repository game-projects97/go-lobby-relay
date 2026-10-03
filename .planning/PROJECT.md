# Go Lobby & Relay Server

## What This Is

소규모 멀티플레이 게임을 위한 Go 기반 Room/Lobby, Quick Match, 인증된 UDP Relay 백엔드다. 플레이어는 짧은 수명의 Player Token으로 공개·비공개 로비를 만들고 검색·입장·퇴장·준비할 수 있다. 로비 시작 또는 빠른 매칭이 성사되면 서버는 이미 구현된 Relay room과 참가자별 grant를 할당한다.

초기 릴리스는 외부 저장소가 없는 단일 Go 프로세스다. 게임 로직, 물리, Unity Headless를 실행하지 않으며 Steamworks, FishNet, Open Match는 선택적 후속 adapter다.

## Core Value

플레이어가 방을 만들거나 빠르게 매칭되고, 매칭된 참가자만 안전하게 같은 UDP Relay room에서 통신할 수 있다.

## Requirements

### Validated

- [x] Go/C# bounded Protobuf Relay 계약과 재현 가능한 생성 절차
- [x] 운영자용 immutable Relay room/grant 수명주기
- [x] 인증·replay 방지·rebind·same-room UDP fan-out
- [x] 입력·rate·fan-out hard limit과 secret-free cleanup

### Completed in M1

- [x] 운영자가 플레이어 ID에 묶인 짧은 수명의 Player Token을 발급한다.
- [x] 플레이어가 public/private Lobby를 생성·검색·조회·입장·퇴장한다.
- [x] Lobby owner와 ready 상태가 동시성·revision 충돌에도 일관되게 유지된다.
- [x] owner가 full/all-ready Lobby를 기존 Relay allocation으로 시작한다.
- [x] 플레이어가 동일 queue/capacity의 bounded FIFO Quick Match에 참가·조회·취소한다.
- [x] 매칭된 각 플레이어는 자기 Relay grant만 받는다.
- [x] 전체 HTTP→Relay 흐름이 Unity 없이 자동 검증된다.

### Out of Scope

- Unity Headless, 서버 물리·충돌·게임 규칙, authoritative simulation
- Steamworks/FishNet/Open Match runtime의 M1 도입
- skill rating, party, team, region, backfill, invite, chat, presence
- Redis, persistence, multi-process state, Kubernetes, Agones
- WebGL/WebRTC, reliable/ordered gameplay transport (WebSocket Relay carrier는 [ADR 0004](../docs/decisions/0004-relay-carriers-and-match-ttl.md)로 선택 기능이 되었다)
- 고정 Unity Editor patch나 실기기 지원 주장의 M1 선행 조건화

## Constraints

- **Runtime:** 단일 Go 바이너리와 인메모리 상태
- **Dependencies:** 표준 라이브러리 우선, 기존 Protobuf와 `x/time/rate`만 유지
- **Identity:** player API는 body/path의 player ID를 신뢰하지 않고 검증된 Player Token만 사용
- **Network:** private operator HTTP, player HTTP, UDP Relay listener를 분리
- **Security:** token/grant/payload를 로그에 남기지 않고 trust 이전에 hard limit 적용
- **Lifecycle:** 프로세스 재시작 시 token, Lobby, ticket, assignment, Relay state 모두 소멸
- **Deployment:** Redis/Kubernetes 없이 단일 VM 또는 Docker host부터 운영

## Key Decisions

| Decision | Outcome |
|---|---|
| UDP Relay Phase 1–3 구현은 폐기하지 않고 post-match transport로 유지 | Accepted — 2026-08-20 |
| M1을 Room/Lobby & Quick Match backend로 재정의 | Accepted — 2026-08-20 |
| operator secret + process nonce로 짧은 수명의 Player Token 발급 | Accepted — 2026-08-20 |
| mutable Lobby state는 별도 single-lock Manager가 소유 | Accepted — 2026-08-20 |
| Quick Match는 동일 queue/capacity의 1인 FIFO ticket만 지원 | Accepted — 2026-08-20 |
| player와 operator HTTP listener 분리 | Accepted — 2026-08-20 |
| Unity version/device matrix는 실제 client integration Phase에서 결정 | Accepted — 2026-08-20 |
| Steamworks/FishNet/Open Match는 검증된 필요가 생길 때 adapter로 추가 | Deferred |
| 브라우저 client용 WebSocket Relay carrier와 `--match-ttl`, matched ticket release ([ADR 0004](../docs/decisions/0004-relay-carriers-and-match-ttl.md)) | Accepted — 2026-09-30 |

## Context

- Phase 1–3 evidence: [phase-1](../docs/evidence/m1/phase-1.md), [phase-2](../docs/evidence/m1/phase-2.md), [phase-3](../docs/evidence/m1/phase-3.md)
- Approved redesign: [Room/Lobby & Quick Match design](../docs/superpowers/specs/2026-08-20-room-lobby-quick-match-design.md)
- Execution plan: [M1 implementation plan](../docs/superpowers/plans/2026-08-20-room-lobby-quick-match-m1.md)
- Completion evidence: [Milestone 1](../docs/evidence/m1/milestone-1.md)
- ADR 0001–0003 remain authoritative for the implemented Relay wire, lifecycle, and UDP admission contracts. ADR 0004 adds the optional WebSocket carrier behind the same Relay core.
- First real client: [avoid-ball-multiplayer](https://github.com/game-projects97/avoid-ball-multiplayer) web race mode (TypeScript, Quick Match + WebSocket carrier).

---
*Last updated: 2026-09-30 after ADR 0004 acceptance and first web client integration*
