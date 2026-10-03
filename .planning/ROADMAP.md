# Roadmap: Go Lobby & Relay Server

## Overview

완료된 Relay 기반을 유지하면서 player identity, mutable Lobby와 FIFO Quick Match를 같은 단일 Go 프로세스에 추가한다. M1은 Unity build 없이 backend 전체 HTTP→UDP 흐름을 닫는다. 실제 client integration과 Unity 지원 matrix, 운영·패키징·성능은 M2가 소유한다.

## Milestones

- [x] **Milestone 1 — Room/Lobby & Quick Match MVP** (Phases 1–5) — Complete 2026-08-20
- [ ] **Milestone 2 — Client Integration & Single-Host Operation** (Phases 6–9)

## Phase Details

### Phase 1: Wire Contract and Threat Boundary

**Goal:** Go와 C#이 동일한 bounded Relay wire/security contract를 사용한다.

**Mode:** mvp

**Depends on:** Nothing

**Requirements:** PROT-01, PROT-02

**Status:** Complete — 2026-08-09

**Plan:** [Phase 1 plan](../docs/superpowers/plans/2026-08-09-phase-1-wire-contract.md)

**Evidence:** [Phase 1 evidence](../docs/evidence/m1/phase-1.md)

### Phase 2: Relay Room and Session Kernel

**Goal:** authenticated operator가 immutable Relay room과 grant lifecycle을 안전하게 제어한다.

**Mode:** mvp

**Depends on:** Phase 1

**Requirements:** ROOM-01, ROOM-02, SESS-01

**Status:** Complete — 2026-08-09

**Plan:** [Phase 2 plan](../docs/superpowers/plans/2026-08-09-phase-2-in-memory-room-session.md)

**Evidence:** [Phase 2 evidence](../docs/evidence/m1/phase-2.md)

### Phase 3: Authenticated UDP Relay

**Goal:** authenticated bound participant만 bounded resource로 same-room opaque UDP payload를 중계한다.

**Mode:** mvp

**Depends on:** Phase 2

**Requirements:** ROOM-03, SESS-02~04, RELY-01~03, SAFE-01~03

**Status:** Complete — 2026-08-10

**Plan:** [Phase 3 plan](../docs/superpowers/plans/2026-08-09-phase-3-authenticated-udp-relay.md)

**Evidence:** [Phase 3 evidence](../docs/evidence/m1/phase-3.md)

### Phase 4: Player Identity and Lobby Lifecycle

**Goal:** authenticated player가 public/private Lobby를 create/search/get/join/leave/ready/start하고 모든 mutation이 exact deadline, revision과 hard limit을 지킨다.

**Mode:** mvp

**Depends on:** Phase 3

**Requirements:** AUTH-01, LOBBY-01, LOBBY-02, LOBBY-03, LOBBY-04

**Success Criteria:**

1. operator가 발급한 15분 Player Token만 player identity를 결정하며 tamper, expiry와 restart 후 token이 거부된다.
2. public/private visibility, bounded list, membership exclusivity, capacity, owner transfer와 empty close가 원자적으로 동작한다.
3. ready mutation은 revision을 검사하고 membership change가 ready를 reset하며 owner만 full/all-ready Lobby를 시작한다.
4. successful start만 기존 Relay room과 caller-private assignment를 만든다.
5. focused, concurrent, race와 HTTP integration tests가 통과한다.

**Status:** Complete — 2026-08-20

**Plan:** [M1 Phase 4–5 plan](../docs/superpowers/plans/2026-08-20-room-lobby-quick-match-m1.md)

**Evidence:** [Phase 4 evidence](../docs/evidence/m1/phase-4.md)

### Phase 5: Quick Match and Relay Assignment

**Goal:** single-player FIFO tickets가 compatible group을 정확히 한 Relay match로 만들고 participant-private assignment를 제공한다.

**Mode:** mvp

**Depends on:** Phase 4

**Requirements:** MATCH-01, MATCH-02, MATCH-03

**Success Criteria:**

1. exact `queue_key`와 capacity가 같은 live tickets만 FIFO로 match된다.
2. cancel, exact expiry, capacity와 per-player ownership이 partial state 없이 적용된다.
3. allocation failure가 ticket order를 보존하고 concurrent enqueue가 duplicate match/player/room을 만들지 않는다.
4. Lobby start와 Quick Match 양쪽 HTTP→private grant→UDP bind→same-room exchange가 자동 end-to-end로 통과한다.

**Status:** Complete — 2026-08-20

**Plan:** [M1 Phase 4–5 plan](../docs/superpowers/plans/2026-08-20-room-lobby-quick-match-m1.md)

**Evidence:** [Phase 5 evidence](../docs/evidence/m1/phase-5.md)

#### Milestone 1 Completion Gate

- [x] PROT-01부터 MATCH-03까지 M1 requirement 23개가 evidence에 매핑되어 통과한다.
- [x] 단일 Go process가 operator HTTP, player HTTP, Lobby/Quick Match, Relay store와 UDP Relay를 외부 상태 없이 실행한다.
- [x] 두 독립 client가 Lobby 및 Quick Match 경로에서 자기 grant만 받아 UDP payload를 교환한다.
- [x] Steamworks, FishNet, Open Match runtime, Redis, database, Kubernetes, Agones와 Unity build가 M1 실행 경로에 없다.

**Evidence:** [Milestone 1 evidence](../docs/evidence/m1/milestone-1.md)

### Phase 6: Client SDK Integration

**Goal:** 실제 game client가 정해진 뒤 C# API/Relay lifecycle을 통합하고 주장할 platform matrix를 검증한다.

**Mode:** mvp

**Depends on:** Milestone 1

**Requirements:** UNITY-01, UNITY-02, UNITY-03

**Status:** Not started for UNITY-01~03 — exact Unity/editor/device/network matrix intentionally undecided. 첫 실제 client는 Unity가 아닌 [avoid-ball-multiplayer](https://github.com/game-projects97/avoid-ball-multiplayer) web race mode이며, [ADR 0004](../docs/decisions/0004-relay-carriers-and-match-ttl.md) WebSocket carrier로 Quick Match→Relay 경로를 사용한다. 이 연동은 UNITY requirement evidence로 계산하지 않는다.

### Phase 7: Single-Host Runtime Operations

**Goal:** configuration, private status, structured operations와 bounded drain을 제공한다.

**Mode:** mvp

**Depends on:** Phase 6

**Requirements:** OPS-01, OPS-02, OPS-03, OPS-04

**Status:** Not started

### Phase 8: Static Packaging and Host Deployment

**Goal:** reproducible CGO-free Linux artifact와 최소 container를 한 host에 안전하게 배포·복구한다.

**Mode:** mvp

**Depends on:** Phase 7

**Requirements:** SHIP-01, SHIP-02, SHIP-03

**Status:** Not started — CI(`ci.yml`), 단일 Lightsail VM Terraform/systemd 배포 기반(`99e9242`, `6b1cdc6`)과 Caddy WebSocket TLS front(`8d80ece`)가 선행 작업으로 존재하지만 SHIP-01~03 evidence는 아직 없다.

### Phase 9: Failure Drills and Performance Evidence

**Goal:** failure recovery와 named load/soak profile의 correctness·resource·latency 주장을 재현한다.

**Mode:** mvp

**Depends on:** Phase 8

**Requirements:** VERI-01, VERI-02, PERF-01, PERF-02

**Status:** Not started

#### Milestone 2 Completion Gate

- [ ] 실제 client integration matrix와 lifecycle evidence가 통과한다.
- [ ] 한 Docker host에서 같은 artifact를 configure, observe, drain, upgrade와 rollback한다.
- [ ] failure/load/soak evidence가 resource와 latency 목표를 명시적으로 pass/fail 판정한다.

## Requirement Coverage

| Phase | Milestone | Requirement Count |
|---|---|---:|
| 1. Wire Contract and Threat Boundary | M1 | 2 |
| 2. Relay Room and Session Kernel | M1 | 3 |
| 3. Authenticated UDP Relay | M1 | 10 |
| 4. Player Identity and Lobby Lifecycle | M1 | 5 |
| 5. Quick Match and Relay Assignment | M1 | 3 |
| 6. Client SDK Integration | M2 | 3 |
| 7. Single-Host Runtime Operations | M2 | 4 |
| 8. Static Packaging and Host Deployment | M2 | 3 |
| 9. Failure Drills and Performance Evidence | M2 | 4 |
| **Total** | **2 milestones** | **37** |

## Progress

**Execution Order:** Phase 1 → 2 → 3 → 4 → 5 → M1 gate → Phase 6 → 7 → 8 → 9 → M2 gate

| Phase | Plans Complete | Status | Completed |
|---|---:|---|---|
| 1 | 1/1 | Complete | 2026-08-09 |
| 2 | 1/1 | Complete | 2026-08-09 |
| 3 | 1/1 | Complete | 2026-08-10 |
| 4 | 1/1 | Complete | 2026-08-20 |
| 5 | 1/1 | Complete | 2026-08-20 |
| 6 | 0/unplanned | Not started | - |
| 7 | 0/unplanned | Not started | - |
| 8 | 0/unplanned | Not started | - |
| 9 | 0/unplanned | Not started | - |
