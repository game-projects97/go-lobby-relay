# ADR 0004: Pluggable Relay carriers (WebSocket) and configurable match TTL

- **Status:** Proposed
- **Date:** 2026-09-28
- **Decision owners:** Product, Protocol/Security, Operations
- **Supersedes:** the PRD §4 non-goal "WebGL/WebSocket/WebRTC" for the WebSocket carrier only
- **Related requirements:** RELY-01, SAFE-01, SAFE-02, MATCH-02

## Context

Browsers and Android WebViews cannot open UDP sockets, so a web client could reach the Lobby and Quick Match HTTP APIs but never the Relay. Formed matches also expired after a fixed 2 minutes, shorter than a typical round.

The Relay must stay game-agnostic: it must not learn payload formats, rules, tick rates or reliability schemes of any particular client.

## Decision

1. **Carrier seam, not a second Relay.** `internal/relaytransport.Transport` (`Owns`, `Send`) is the only contract a non-UDP carrier implements. `udprelay.Server` stays the single Relay core: every carrier's datagram goes through `Deliver` into the same decode → handshake/HMAC/replay → admission → fan-out path. Fan-out routes each recipient to the carrier that owns its endpoint, so a UDP client and a WebSocket client can share a room.
2. **Endpoint namespace.** A carrier tags its connections with `relaytransport.Tag(peer, zone)` — the peer address in IPv6 form with a carrier zone (`~ws`). UDP never produces that zone and `Run` drops any UDP datagram that would fall into a carrier namespace, so endpoints cannot collide or be spoofed across carriers. `relayroom` strips the zone when keying pre-auth budgets, so a peer IP shares one budget across carriers.
3. **WebSocket carrier (`internal/wsrelay`).** Optional, enabled by `--relay-ws-listen`. Route `/v1/relay`, subprotocol `relay.v1`, binary messages only, each message exactly one `relay.v1` Envelope (≤ 1200 bytes; larger closes with 1009). Delivery stays best-effort: a 32-deep per-connection queue drops when full and never blocks fan-out. Cross-origin browsers need `--relay-ws-allowed-origin` (host pattern, repeatable; `*` is rejected). Connections are capped at 1024 overall and `--relay-ws-max-per-source` (default 16) per peer IP; raise the latter behind a TLS-terminating reverse proxy, where every client shares the proxy IP. Idle connections close after 75s (binding TTL is 60s).
4. **Match TTL.** `--match-ttl` (Go duration, default `2m`, maximum `2h` = Relay room hard maximum) bounds formed match rooms, grants and assignments.

Added direct dependency: `github.com/coder/websocket` (zero transitive dependencies).

## Non-decisions

- No payload parsing, reliability, ordering or gameplay validation in the Relay. Clients own redundancy/ACK and result verification.
- No trusted `X-Forwarded-For`; pre-auth budgets use the TCP peer address.
- No advertised WebSocket URL in assignments; clients are configured with it.
- WebRTC/WebTransport remain out of scope; they would implement the same `Transport` seam.
