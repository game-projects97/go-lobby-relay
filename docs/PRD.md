# [PRD v5.0] Go Room/Lobby, Quick Match & UDP Relay Server

| 항목 | 내용 |
|---|---|
| 버전 | 5.0 |
| 갱신일 | 2026-08-20 |
| 상태 | Milestone 1 verified / Phases 1–5 complete / Phases 6–9 pending |
| 제품 | `go-lobby-relay` |
| 주 독자 | 제품 책임자, Go/C# 엔지니어, 초기 운영 담당자 |

> v5.0은 Unity-native-first M1을 대체한다. M1은 Room/Lobby와 Quick Match backend를 자동 HTTP→UDP evidence로 완성한다. 실제 Unity editor patch, device와 network matrix는 실제 game client가 존재하는 Phase 6에서만 결정한다.

## 1. Executive Summary

이 제품은 소규모 멀티플레이 게임을 위한 Go 기반 Room/Lobby, Quick Match와 인증된 UDP Relay 서버다. 플레이어는 operator 또는 미래 identity adapter가 발급한 짧은 수명의 Player Token으로 Lobby를 만들고 검색·입장·퇴장·준비하거나 Quick Match ticket을 제출한다. 매칭이 성사되면 서버는 참가자가 고정된 Relay room을 만들고 각 플레이어에게 자기 grant만 반환한다.

서버는 gameplay state, physics, collision, animation 또는 game rule을 실행하지 않는다. 이미 검증된 Relay는 match 이후 작은 opaque UDP payload를 same-room participant에게 best-effort로 전달한다.

M1은 한 Go process와 in-memory state만 사용한다. Redis, database, Kubernetes, Agones, Open Match runtime, Steamworks SDK, FishNet runtime과 Unity build는 M1 dependency가 아니다.

## 2. 해결하려는 문제

초기 멀티플레이 게임은 거창한 dedicated game server 이전에 다음 backend가 필요하다.

- 플레이어가 public/private room을 만들고 찾고 들어가거나 나갈 수 있어야 한다.
- membership 변화, owner, ready 상태와 start 조건이 concurrent request에도 일관되어야 한다.
- 빠른 매칭 요청이 호환되는 사용자만 중복 없이 묶어야 한다.
- 사용자가 다른 사용자 ID, match assignment 또는 Relay grant를 가져갈 수 없어야 한다.
- match 이후 packet은 같은 room의 authenticated participant에게만 전달되어야 한다.
- malformed/oversized/rate-limited 입력이 다른 Lobby, room 또는 process를 고갈시키지 않아야 한다.
- MVP가 외부 분산 인프라나 특정 game SDK에 결합되면 안 된다.

## 3. 제품 목표

1. operator가 valid player identity에 묶인 short-lived Player Token을 발급한다.
2. authenticated player가 bounded public/private Lobby를 create/search/get/join/leave한다.
3. Lobby owner, ready와 revision이 deterministic lifecycle을 따른다.
4. full/all-ready Lobby를 owner가 Relay match로 시작한다.
5. single player가 동일 queue/capacity FIFO Quick Match에 enqueue/get/cancel한다.
6. formed match가 기존 authenticated Relay room을 만들고 caller-private assignment를 반환한다.
7. 두 독립 client의 Lobby 및 Quick Match HTTP→UDP 흐름을 자동 검증한다.
8. 동일 source를 M2에서 C# client, single-host operation, packaging과 performance evidence로 확장한다.

## 4. 비목표

- Unity Headless, authoritative simulation, physics, game rules, anti-cheat 판단
- party, team, skill rating, region, backfill, host migration, invite, chat, friends, presence
- reliable/ordered gameplay delivery, ACK/retry/offline buffering
- persistence, restart recovery, multi-process ownership, horizontal scaling
- Steamworks/FishNet/Open Match runtime의 M1 직접 통합
- WebGL/WebRTC, P2P hole punching, STUN/TURN/ICE (WebSocket Relay carrier는 [ADR 0004](./decisions/0004-relay-carriers-and-match-ttl.md)로 선택 기능이 되었다)
- fixed Unity version 또는 physical device를 backend M1의 선행 조건으로 지정

## 5. 사용자와 핵심 흐름

### 5.1 사용자

| 사용자 | 필요한 결과 |
|---|---|
| 플레이어 | Lobby를 만들거나 찾아 참여하고 ready 또는 Quick Match로 match assignment를 얻는다. |
| 게임 client 개발자 | operator secret 없이 Player Token과 자기 Relay grant만 사용한다. |
| operator/identity adapter | player identity를 검증한 뒤 짧은 Player Token을 발급한다. |
| Go 엔지니어 | Lobby와 Relay 경계를 독립적으로 테스트하고 한 process로 조립한다. |
| 운영자 | M2에서 한 VM/Docker host의 동일 artifact를 설정·관찰·종료한다. |

### 5.2 Lobby 흐름

1. trusted operator가 `player_id`에 묶인 15분 Player Token을 발급한다.
2. player가 public/private Lobby를 생성한다. creator는 owner이자 not-ready member다.
3. 다른 player가 public list에서 찾거나 exact private ID로 join한다.
4. membership change는 모든 ready를 false로 reset하고 revision을 증가시킨다.
5. full/all-ready 상태에서 owner가 start한다.
6. 서버가 immutable Relay room을 할당하고 각 player가 자기 assignment만 조회한다.
7. client가 Relay handshake 후 same-room UDP payload를 교환한다.

### 5.3 Quick Match 흐름

1. player가 `queue_key`와 target `capacity`로 single-player ticket을 생성한다.
2. 서버가 같은 key/capacity의 live tickets를 FIFO로 선택한다.
3. group이 채워지면 한 Relay room을 만들고 ticket을 matched로 바꾼다.
4. player는 자기 ticket status와 assignment만 조회한다.
5. allocation 실패 시 selected tickets와 FIFO order는 그대로 유지된다.

### 5.4 종료와 restart

- exact deadline에서 authority가 종료되며 sweep 전에도 stale mutation이 거부된다.
- empty/expired Lobby와 ticket/assignment가 bounded sweep으로 제거된다.
- process restart는 Player Token, Lobby, ticket, assignment, Relay room과 grant를 모두 무효화한다.

## 6. Milestones

### M1 — Room/Lobby & Quick Match MVP

**Phases:** 1–5

**결과:** separate operator HTTP, player HTTP와 UDP Relay listener를 가진 한 Go process가 identity, Lobby, Quick Match와 authenticated packet exchange를 외부 상태 없이 완료한다.

### M2 — Client Integration & Single-Host Operation

**Phases:** 6–9

**결과:** 실제 client integration matrix를 정하고 동일 source를 observable, reproducible, deployable single-host artifact로 만들며 failure/load evidence를 남긴다.

## 7. M1 Requirements

### 7.1 완료된 Relay foundation — 15개

- [x] `PROT-01`, `PROT-02`: bounded Go/C# Protobuf contract와 reproducible generation
- [x] `ROOM-01`, `ROOM-02`, `ROOM-03`: immutable Relay allocation, operator control, cleanup
- [x] `SESS-01`, `SESS-02`, `SESS-03`, `SESS-04`: grant, bind/rebind, HMAC/replay, pre-auth safety
- [x] `RELY-01`, `RELY-02`, `RELY-03`: same-room byte-preserving best-effort fan-out와 bounded failure
- [x] `SAFE-01`, `SAFE-02`, `SAFE-03`: hard limits, hierarchical budgets와 safe drop classification

Evidence: [Phase 1](./evidence/m1/phase-1.md), [Phase 2](./evidence/m1/phase-2.md), [Phase 3](./evidence/m1/phase-3.md)

### 7.2 Player Identity and Lobby — 5개

- [x] **AUTH-01:** operator-issued 15-minute Player Token is the only player identity authority; tamper, expiry and restart invalidate it.
- [x] **LOBBY-01:** create public/private Lobby and bounded public search or authorized exact get.
- [x] **LOBBY-02:** atomic join/leave, one active ownership, capacity, deterministic owner transfer and empty close.
- [x] **LOBBY-03:** revision-checked ready, membership reset and owner-only full/all-ready start.
- [x] **LOBBY-04:** exact deadline, hard limit, privacy, cleanup, redaction and concurrent mutation safety.

Evidence: [Phase 4](./evidence/m1/phase-4.md)

### 7.3 Quick Match and assignment — 3개

- [x] **MATCH-01:** compatible one-player FIFO ticket, status, cancellation and exact expiry.
- [x] **MATCH-02:** Lobby/Quick Match creates immutable Relay allocation and exposes caller-private assignment only.
- [x] **MATCH-03:** allocation rollback preserves tickets and concurrency creates no duplicate match/player/room.

Evidence: [Phase 5](./evidence/m1/phase-5.md)

The authoritative 37-requirement registry is [REQUIREMENTS.md](../.planning/REQUIREMENTS.md).

## 8. Product Limits

| Boundary | M1 hard maximum/default |
|---|---:|
| Player Token TTL | exactly 15 minutes |
| Open Lobbies | 256 |
| Members per Lobby | 16 |
| Lobby capacity | 2–16 |
| Lobby TTL | default 30 minutes, maximum 2 hours |
| Public list page | 50 |
| Live Quick Match tickets | 4096 |
| Ticket TTL | 2 minutes |
| Assignment/Relay match TTL | default 2 minutes, `--match-ttl` maximum 2 hours |
| WebSocket carrier connections | 1024, per peer IP default 16 (`--relay-ws-max-per-source`) |
| Player active ownership | one open Lobby or one live ticket |
| Relay datagram / opaque payload | 1200 / 900 bytes |
| Existing Relay rooms / sessions | 256 / 4096 |

Trust-boundary validation and rate limits apply before mutation or allocation. Future configuration may lower positive finite maxima but cannot disable them.

## 9. API Product Surface

### Operator listener

- `POST /v1/player-tokens`
- existing `PUT|GET|DELETE /v1/rooms/{room_id}`

### Player listener

- `POST|GET /v1/lobbies`
- `GET /v1/lobbies/{lobby_id}`
- `POST /v1/lobbies/{lobby_id}/join`
- `DELETE /v1/lobbies/{lobby_id}/members/me`
- `PUT /v1/lobbies/{lobby_id}/members/me/ready`
- `POST /v1/lobbies/{lobby_id}/start`
- `POST /v1/matchmaking/tickets`
- `GET|DELETE /v1/matchmaking/tickets/me`

The player listener never serves operator routes. Player identity always comes from the verified Bearer token. Responses use `Cache-Control: no-store` and never expose another participant's grant.

## 10. M1 Acceptance Gate

M1 is complete only when all are true:

- [x] 23 M1 requirements have implementation and named evidence.
- [x] one process owns operator HTTP, player HTTP, Lobby/Match state, Relay state and UDP Relay.
- [x] create→join→ready→start produces two private grants and successful UDP exchange.
- [x] enqueue→matched produces two private grants and successful UDP exchange.
- [x] expiry, tamper, identity confusion, privacy, capacity, allocation rollback and concurrency tests pass.
- [x] protocol generation, all Go tests, race, fuzz, vet and binary build pass on a clean candidate.
- [x] no Redis/database/distributed runtime/game SDK/Unity build is required.

Evidence: [Milestone 1](./evidence/m1/milestone-1.md)

## 11. M2 Scope

- Phase 6: actual C# game client integration and evidence-driven Unity/platform matrix
- Phase 7: full configuration precedence, private status, structured operations and bounded drain
- Phase 8: CGO-free Linux artifact, minimal non-root/read-only container and runbook
- Phase 9: failure drills, checked-in load client, latency/loss/throughput/CPU/RSS/startup report

Resource targets (`RSS <= 20MB`, `CPU <= 2%`, startup p95 `<= 50ms`) are goals for an approved named Phase 9 profile, not universal guarantees.

## 12. Risks and Mitigation

| Risk | Mitigation |
|---|---|
| caller impersonates another player | body/path identity ignored; HMAC Player Token claims are authoritative |
| public API exposes operator authority | separate listeners and route sets |
| Lobby and Relay lifecycle diverge | match freezes members then calls one existing immutable allocator under a single direction of locking |
| concurrent join/start duplicates state | one Lobby Manager lock, revision checks and mutation-after-allocation |
| allocation failure loses queue order | no ticket mutation until Relay allocation succeeds |
| restart surprises clients | explicit in-memory state-loss contract and process-nonce token invalidation |
| fixed Unity version blocks backend | exact client matrix deferred until a real client exists |
| MVP grows into general matchmaking | only one-player FIFO exact key/capacity; new rules require new requirements |

## 13. Dependency and Integration Policy

- **Steamworks:** future identity/invite adapter; not a Lobby domain authority.
- **FishNet:** future client/game networking adapter after assignment; not a Go gameplay runtime.
- **Open Match:** future replacement when ticket/pool/function needs exceed bounded FIFO.
- **Redis/Kubernetes/Agones:** adopt only after measured multi-process demand and explicit consistency semantics.

No placeholder adapter or dependency is added in M1.

## 14. Change Control

- Requirement IDs and status are authoritative in `.planning/REQUIREMENTS.md`.
- Phase ordering and milestone gates are authoritative in `.planning/ROADMAP.md`.
- Phase 1–3 ADRs remain authoritative for existing Relay contracts.
- Phase 4–5 implementation follows [the approved design](./superpowers/specs/2026-08-20-room-lobby-quick-match-design.md) and [execution plan](./superpowers/plans/2026-08-20-room-lobby-quick-match-m1.md).
- Checkboxes change only after clean-candidate verification evidence exists.
