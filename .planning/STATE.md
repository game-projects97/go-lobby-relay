---
gsd_state_version: '1.0'
status: in_progress
progress:
  total_phases: 9
  completed_phases: 5
  total_plans: 5
  completed_plans: 5
  percent: 56
---

# Project State

## Project Reference

See: [PROJECT.md](./PROJECT.md)

**Core value:** 플레이어가 방을 만들거나 빠르게 매칭되고, 매칭된 참가자만 안전하게 같은 UDP Relay room에서 통신할 수 있다.

**Current focus:** Phase 6 — Client SDK Integration planning (first real client: avoid-ball-multiplayer web race mode)

## Current Position

Phase: 6 of 9

Plan: not yet written; Unity target still unchosen. A non-Unity web client already integrates through the ADR 0004 WebSocket carrier.

Status: Milestone 1 complete; post-M1 carrier/infra work merged; Milestone 2 requirements not yet evidenced

Last activity: 2026-09-30 — ADR 0004 accepted; planning docs synced with post-M1 work

Progress: [██████░░░░] 56%

## Completed Foundation

- Phase 1: bounded Protobuf contract, Go/C# generation and threat boundary
- Phase 2: operator-authenticated immutable Relay room/grant lifecycle
- Phase 3: authenticated UDP bind/rebind/replay/admission/fan-out and minimum server binary
- Phase 4: operator-issued Player Token and bounded public/private Lobby lifecycle
- Phase 5: one-player FIFO Quick Match, private Relay assignments, and real HTTP→UDP flows

Evidence: [Phase 1](../docs/evidence/m1/phase-1.md), [Phase 2](../docs/evidence/m1/phase-2.md), [Phase 3](../docs/evidence/m1/phase-3.md), [Phase 4](../docs/evidence/m1/phase-4.md), [Phase 5](../docs/evidence/m1/phase-5.md), [M1](../docs/evidence/m1/milestone-1.md)

## Post-M1 Work (merged to main, outside the phase plans)

- Package/API renames and shared strict JSON/admission helpers (`fa35aa5`, `04053d8`, `1a1d375`)
- CI (gofmt, vet, race tests, govulncheck) and guarded single-VM Lightsail deployment groundwork (`99e9242`, `6b1cdc6`)
- [ADR 0004](../docs/decisions/0004-relay-carriers-and-match-ttl.md): pluggable Relay carriers with optional WebSocket carrier, `--match-ttl`, matched ticket release (`81f28fe`, `233d21a`)
- Caddy TLS front for the WebSocket carrier (`8d80ece`)
- First client: [avoid-ball-multiplayer](https://github.com/game-projects97/avoid-ball-multiplayer) 1:1 race mode over Quick Match + WebSocket carrier

## Current Decisions

- Existing Relay implementation is the post-match transport and remains unchanged in responsibility.
- M1 completes Room/Lobby and one-player FIFO Quick Match before any Unity build requirement.
- Player identity uses operator-issued 15-minute HMAC Player Tokens with a process nonce, so restart invalidates existing tokens.
- Player HTTP, operator HTTP and UDP Relay use separate listeners in one process.
- Lobby membership is mutable before match; Relay room participants are immutable after allocation.
- Steamworks, FishNet and Open Match are optional future adapters, not M1 dependencies.
- Browser clients use the WebSocket carrier behind the same Relay core; the Relay stays payload-agnostic (ADR 0004, Accepted 2026-09-30).

## Pending Work

1. Decide whether Phase 6 stays Unity-scoped (UNITY-01~03) or is re-scoped to cover the web client already in use; record the decision before writing the plan.
2. Choose the Unity client target (if kept) before fixing any Unity editor/platform/device matrix.
3. Turn the existing CI/deploy groundwork into Phase 7–8 plans and evidence (OPS-01~04, SHIP-01~03) rather than treating it as complete.
4. Keep Steamworks and FishNet optional until the selected client requires their adapter boundaries.

## Blockers/Concerns

- No external blocker. Loopback socket tests require the already approved non-sandbox execution environment.
- Phase 9 must define a named host/load profile before resource targets can be interpreted.

## Deferred Items

| Category | Item | Status |
|---|---|---|
| Client | exact Unity editor/platform/device/network matrix | Phase 6 decision after actual client exists |
| Identity | Steamworks validation adapter | post-M1 candidate |
| Transport | FishNet integration | Phase 6/post-M1 candidate |
| Transport | WebRTC/WebTransport carriers | out of scope; would implement the ADR 0004 `Transport` seam |
| Matchmaking | party/skill/region/team/backfill or Open Match | post-M1 candidate |
| Infrastructure | Redis, persistence, multi-instance, Kubernetes, Agones | demand-driven future candidate |

## Session Continuity

Last session: 2026-09-30

Stopped at: ADR 0004 accepted and planning docs synced; Phase 6 scope decision is next

Resume file: [ADR 0004](../docs/decisions/0004-relay-carriers-and-match-ttl.md), [Milestone 1 evidence](../docs/evidence/m1/milestone-1.md)
