# Architecture Patterns

**Domain:** Go-based ultra-light UDP game relay/session server
**Project:** Go Lightweight Game Relay
**Researched:** 2026-08-08
**Overall confidence:** MEDIUM — recommendations are grounded in current Go/Unity documentation and IETF RFCs; the remaining uncertainty is target mobile-network behavior and measured single-host capacity.

## Recommended Architecture

Build one boring process with one in-memory state owner, one UDP socket, and one management HTTP listener. The HTTP and UDP adapters call the same store; they never own room/session state themselves. Do not add Redis, a message bus, per-room sockets, a room actor framework, gRPC, or an Open Match runtime dependency.

```text
 operator / future Director                         Unity native clients
            | HTTPS                                         | UDP
            v                                               v
  +---------------------+                         +----------------------+
  | management HTTP API |                         | UDP receive/relay    |
  | PUT / rooms         |                         | bind + opaque fanout |
  | GET / DELETE        |                         +----------+-----------+
  | livez / readyz      |                                    |
  +----------+----------+                                    |
             |                                               |
             +-------------------+---------------------------+
                                 v
                   +-----------------------------+
                   | in-memory room/session store|
                   | one mutex, all indexes      |
                   | rooms, grants, bindings     |
                   +--------------+--------------+
                                  ^
                                  |
                   +--------------+--------------+
                   | one expiry sweeper          |
                   | room/grant/binding deadlines|
                   +-----------------------------+

  main/server lifecycle owns all components and closes them in a fixed order.
```

The future Director seam is the stable `PUT /v1/rooms/{room_id}` request/response contract. A future Open Match adapter may translate a Match into that call; the relay should not contain a `Director` interface or import Open Match packages now.

### Component Boundaries

| Component | Responsibility | Owns mutable state | Communicates With |
|-----------|----------------|--------------------|-------------------|
| `cmd/lobby-relay` composition root | Parse/validate configuration, construct components, install signal context, choose exit code | Process lifecycle only | Server lifecycle |
| Server lifecycle | Start HTTP, UDP, and sweeper; expose readiness; coordinate bounded shutdown | Readiness/draining flags | HTTP adapter, UDP relay, sweeper, store |
| Management HTTP adapter | Authenticate operator, bound/decode requests, map store errors to JSON/HTTP, serve health | None | Store, readiness |
| UDP relay adapter | Read one datagram, enforce byte/type/version bounds, run bind protocol, authorize data, fan out one encoded packet | Reusable receive buffer and socket only | Protocol codec, store, `net.UDPConn` |
| Protocol codec | Generated Protobuf types plus small validation/canonical-HMAC helpers | None | UDP relay, Unity-generated contract |
| Room/session store | Atomically create/end rooms, issue grants, validate challenges, activate/rebind sessions, snapshot recipients, expire state | **All** rooms, sessions, grants, pending challenges, binding indexes and rate counters | HTTP, UDP, sweeper |
| Expiry sweeper | Periodically call `Expire(now)`; no timer per room/session | Ticker only | Store |
| Unity `RelayClient` sample | Socket and bind state machine, generated C# messages, background receive, main-thread delivery, pause/resume rebind | Client-local connection state | Relay UDP endpoint, game code |
| Go load generator | Create synthetic clients/rooms and report offered load, latency and loss; never linked into server | Test-run state | HTTP API, UDP endpoint |

### State Ownership and Concurrency

- Put every mutable map behind one `sync.RWMutex` in the store. Maintain `roomsByID`, `grantsByID`, and `bindingsByID` together so an operation cannot update one index without the others.
- Start with one UDP read/dispatch goroutine. It performs bounded parsing, one short store operation, one marshal, and sequential best-effort writes to a copied recipient list. It must release the store lock before marshaling, logging, or network I/O.
- Let `net/http` use its normal handler goroutines. Store calls remain short and atomic. A single sweeper goroutine calls `Expire(now)` on a ticker.
- Do not create a goroutine, channel, socket, timer, or queue per room. The kernel UDP receive buffer is the initial bounded queue; overload is observed as loss rather than unbounded heap growth.
- Canonicalize source addresses with `netip.Addr.Unmap()` and store comparable `netip.AddrPort` values. Go documents that packet-connection methods are safe concurrently, so the UDP adapter can later add a fixed worker model without changing store or protocol boundaries if benchmarks justify it.
- Pass `now time.Time` into state transitions that need time. This keeps expiry tests deterministic without introducing a clock interface.

### Boring Package Layout

```text
api/relay/v1/relay.proto           # single wire-contract source
cmd/lobby-relay/main.go            # config and composition only
cmd/relay-load/main.go             # independent load client
internal/app/server.go             # lifecycle/start/drain/stop
internal/httpapi/operatorapi/      # operator room and token handlers
internal/httpapi/playerapi/        # player lobby and ticket handlers
internal/udprelay/udp.go           # datagram loop and fanout
internal/relayroom/                # room/session state owner
internal/matchmaking/              # lobby, ticket and match state owner
internal/protocol/codec.go         # bounds + canonical bind transcript
gen/go/relay/v1/...                # generated Go messages
unity/RelaySample/Assets/Relay/... # client + generated C# messages
deploy/relay.service               # VM example
Dockerfile                         # CGO-free image
```

Avoid `repository`, `service`, `factory`, event-bus, and one-implementation interface layers. Tests can use loopback sockets and real store values directly.

## Data Flow

### 1. Room Allocation / Control Plane

1. An authenticated operator calls `PUT /v1/rooms/{room_id}` with an immutable room definition: participant/session IDs, room expiry, credential expiry, and bounded participant count.
2. The caller supplies the stable room ID (a future Director can derive it from a Match ID). Repeating the same request returns the same room and still-live join grants; a different definition for the same ID returns `409 Conflict`. This uses HTTP semantics instead of a custom idempotency framework.
3. The store generates each join secret with 32 bytes from `crypto/rand`. The response contains the advertised UDP endpoint, protocol major version, grant ID, join secret, and expiry. `GET` responses and logs never include secrets or payloads.
4. The matchmaker/operator conveys each allocation to the intended Unity client over its already-secure control channel. Management HTTP must be authenticated and either terminated with TLS or restricted to a private/loopback network behind a TLS proxy.
5. `DELETE /v1/rooms/{room_id}` is idempotent: it marks the room ended, invalidates grants/bindings, and always returns success for an already-ended/missing resource. A short, bounded tombstone retention prevents an immediate retry of the original `PUT` from resurrecting an ended room.

Minimum management surface:

| Endpoint | Behavior |
|----------|----------|
| `PUT /v1/rooms/{room_id}` | Idempotently create room and participant grants; same input returns same allocation, conflicting input is `409` |
| `GET /v1/rooms/{room_id}` | Operator-visible status and counts, with credentials redacted |
| `DELETE /v1/rooms/{room_id}` | Idempotently end room and invalidate every session |
| `GET /livez` | Process is alive; no dependency checks because v1 has no dependencies |
| `GET /readyz` | UDP socket is bound and process is accepting rooms/binds; fails during drain |

Use a bounded JSON body for this low-frequency API. Protobuf remains the Go/Unity UDP contract; adding gRPC to three management operations buys nothing in v1.

### 2. UDP Handshake and Session Binding

Do not send the join secret in a UDP datagram. Use a small HMAC challenge-response that proves both credential possession and return reachability before allowing fan-out:

```text
Unity                                      Relay
  | HELLO(grant_id, client_nonce)            |
  |----------------------------------------->| validate known, live grant
  | CHALLENGE(server_nonce, candidate_id)    | remember one short-lived pending
  |<-----------------------------------------| challenge for observed AddrPort
  | AUTH(candidate_id, HMAC(join_secret,     |
  |      "relay-bind-v1" || fixed fields))  |
  |----------------------------------------->| same AddrPort + timing-safe verify
  | BOUND(candidate_id, binding_expiry)      | atomically activate binding
  |<-----------------------------------------|
```

Rules:

- The fixed HMAC transcript is domain-separated and consists only of fixed-width/versioned fields; never sign an implementation-dependent Protobuf serialization.
- Keep at most one pending challenge per issued session and expire it quickly. A challenge is one-use. Use `hmac.Equal` for verification.
- Before address validation, emit no more than three response bytes per byte received, following the anti-amplification principle specified for QUIC. Invalid, unknown, malformed, oversized, expired, or rate-limited handshakes receive no response.
- A session is not eligible to send or receive relayed payloads until `AUTH` succeeds. A high-entropy candidate binding ID is not a room ID and is never sequential.
- For NAT rebinding, run the same exchange from the new observed endpoint. Keep the old active binding until the new `AUTH` succeeds, then atomically rotate the binding ID/endpoint and invalidate the old ID. A spoofed HELLO therefore cannot disconnect the current client.
- This protects against off-path guessing/spoofing and passive theft of the reusable join secret. It does **not** provide payload confidentiality or full on-path integrity. If that threat becomes required, adopt a reviewed secure transport such as DTLS/QUIC in a later milestone rather than inventing packet encryption now.

### 3. Opaque Packet Relay / Data Plane

1. A bound client sends one binary Protobuf `ClientData` datagram containing protocol major, random binding ID, client sequence/channel metadata, and opaque game payload.
2. Read into a `max_datagram_bytes + 1` buffer. If `n > max_datagram_bytes`, the type/version is unsupported, or decoding fails, drop it before state lookup. Start with a **1200-byte total UDP payload cap**. This is a pragmatic cross-network default informed by IETF MTU guidance and QUIC practice, not a mathematical guarantee against fragmentation on every IPv4/tunneled path; validate and lower it if target-network evidence requires.
3. Look up the binding, require exact canonical source `AddrPort`, enforce credential/room/binding deadlines plus per-session packet/byte and room fan-out budgets, then update `lastSeen`.
4. Under the store lock, copy the live recipient endpoints and authoritative sender participant ID; release the lock.
5. Marshal one `ServerData` datagram using the authoritative sender ID and the original payload bytes unchanged. Write the same byte slice once to every other active recipient. Never trust or forward a client-claimed sender identity.
6. UDP data writes are best-effort: no retry, ordering, deduplication, delivery acknowledgment, or immediate eviction after one write error. Count aggregate outcomes and let consent/idle expiry remove stale bindings.

The server reads only routing/authentication metadata. Sequence meaning, interpolation, reliability, and gameplay semantics remain Unity/game concerns.

### 4. Room, Grant, and Binding Lifecycle

| Object | Live states | Terminal trigger | Cleanup rule |
|--------|-------------|------------------|--------------|
| Room | `OPEN` | Explicit DELETE, room TTL, or no live sessions after the configured empty grace | Remove grants/bindings immediately; retain only bounded idempotency tombstone |
| Join grant | Issued and unexpired | Credential TTL, room end | Remove secret and pending challenge |
| Pending challenge | One candidate endpoint/nonce set | Success, replacement, short challenge TTL, room end | Clear without affecting an existing active binding |
| Active binding | Validated endpoint + random binding ID | Idle/consent timeout, successful rebind, grant/room expiry | Remove binding index; grant may remain usable for rebind until its own expiry |

A freshly created room is not considered empty merely because no client has bound yet; issued live sessions keep it alive. There is one shared UDP socket, so room cleanup is map/index cleanup, not socket cleanup.

Accepted game data is implicit consent/activity. A receive-only participant sends a small authenticated `PING` only when otherwise idle; use jitter, do not send more frequently than needed, and start with an interval no shorter than the IETF UDP keepalive guidance. Stop heartbeats while a mobile app is paused. If the binding expires, run a fresh bind exchange rather than assuming the NAT mapping survived.

### 5. Shutdown and Drain

On `SIGINT`/`SIGTERM`, use one `signal.NotifyContext` cancellation root and a bounded shutdown deadline:

1. Atomically set `draining=true`; `/readyz` fails immediately.
2. Reject management mutations and new UDP `HELLO` messages. Existing reads/status calls may finish.
3. Call `http.Server.Shutdown(ctx)` so active HTTP handlers finish. Independently allow only already-bound UDP traffic for a short bounded grace; do not promise reliable client notification.
4. Close the UDP socket, which unblocks `ReadFromUDPAddrPort`; stop the sweeper and wait for owned goroutines.
5. Exit before Docker/systemd's outer stop timeout. A second signal may force immediate exit.

Process restart intentionally destroys every room/session. Clients detect relay silence or an expired binding and ask the control plane for a current allocation. Drain minimizes partial work; it is not session migration.

## Patterns to Follow

### Pattern 1: One Transactional State Owner

**What:** Keep all related maps and lifecycle transitions in one concrete store guarded by one mutex.

**When:** Always in the single-process milestone.

**Example:**

```go
// Snapshot recipients while locked; perform no encoding or I/O under the lock.
sender, recipients, payload, err := sessions.AuthorizeAndSnapshot(now, src, packet)
if err != nil { return }
out := protocol.ServerData(sender, packet.Sequence, payload)
for _, dst := range recipients { _, _ = conn.WriteToUDPAddrPort(out, dst) }
```

This deliberately prefers a global lock over sharded locks. Upgrade only when contention is measured; the store API is the seam.

### Pattern 2: Bounded Work Before Trust

**What:** Enforce datagram/body size, protocol version, configured room/session counts, and cheap lookup before HMAC, allocation, fan-out, or detailed logging.

**When:** Every HTTP and UDP trust boundary.

Use `http.MaxBytesReader`, server header/read/write/idle timeouts, a participant cap, total room/session caps, per-session packets/bytes per second, and a room/global fan-out circuit breaker. Do not allocate per-source state for random invalid UDP traffic, and do not log every dropped packet.

### Pattern 3: Stable Contract, Raw Payload

**What:** Generate Go and C# code from one `api/relay/v1/relay.proto`; version the envelope explicitly while treating game payload bytes as opaque.

**When:** Every UDP message and cross-language test fixture.

Never reuse field numbers; reserve deleted names/numbers; use binary Protobuf, not ProtoJSON, on UDP. Generate both languages with one checked command and run a breaking-schema check in CI. A protocol-major mismatch is a silent UDP rejection plus a client-visible bind timeout/error, not heuristic decoding.

## Unity Client Contract

- Expose one allocation value: advertised host/port, protocol version, room/session/grant IDs, join secret, and expiries.
- Implement a small state machine: `Disconnected -> Challenging -> Bound -> Rebinding -> Closed`. Only `Bound` may send game payloads.
- Own one socket and one cancellation source. Receive on a background task, decode bounded datagrams there, enqueue plain data, and invoke Unity/game APIs only when the main thread drains the queue.
- On `OnApplicationPause(true)`, stop gameplay sends and cancel/dispose receive work. On resume, create a new socket and rebind; never reuse an assumed public endpoint. Also dispose on normal quit, while recognizing that mobile quit callbacks are not guaranteed.
- If the exact Unity runtime lacks a cancellation-aware socket overload, closing the socket is the compatibility mechanism that unblocks receive. Verify this on every supported Unity/IL2CPP target.
- Surface UDP semantics honestly: packets may be lost, duplicated, or reordered. The sample demonstrates bind, two-client exchange, idle expiry, pause/resume rebind, invalid/expired grant handling, and reacquiring an allocation after process restart.

## Test and Load Harness

| Layer | Smallest valuable check |
|-------|-------------------------|
| Store unit tests | Idempotent create/conflict, room end, grant expiry, pending challenge replay, atomic rebind, empty cleanup |
| Protocol tests | Golden binary fixtures decoded in Go and Unity C#; unsupported version and max+1 byte rejection |
| Go fuzzing | Feed arbitrary datagrams to the bounded decoder and bind state machine; assert no panic/unbounded allocation |
| Race test | Run store + real HTTP/UDP integration tests with `go test -race` |
| Loopback integration | Two/three real UDP clients: successful fan-out, wrong room/grant/source, no pre-auth amplification, expiry/rebind, socket close/shutdown |
| Unity sample test | PlayMode/native builds for at least one PC and one mobile target; pause/resume and main-thread delivery |
| Load generator | Parameters: rooms, participants/room, packet bytes, packets/s, duration; outputs offered/relayed/dropped packets, p50/p95/p99 one-way relay latency, CPU, RSS and socket drops |

Run the load generator as a separate process on a defined host profile. Establish the baseline with the single UDP loop. Only if profiling shows it is the bottleneck, change the UDP adapter to a fixed number of concurrent readers or a bounded worker queue; do not prebuild both modes.

## VM and Docker Deployment

- Build the same artifact with `CGO_ENABLED=0`; bind one UDP port and one management HTTP port. Keep the externally advertised UDP address separate from the listen address because VM/container NAT cannot infer the public endpoint.
- VM: a simple systemd unit with `Restart=on-failure`, an explicit non-root user, environment/credential file permissions, and `TimeoutStopSec` longer than the application's shutdown deadline.
- Docker: minimal/scratch-compatible image, non-root UID, read-only filesystem, exec-form entrypoint, explicit `host:port/udp` publishing, and management TCP published only to loopback/private ingress. Use the binary's tiny healthcheck subcommand or an external HTTP probe; do not add a shell/curl layer solely for health checking.
- Docker's default stop signal is SIGTERM. Keep its stop timeout longer than the application deadline. Benchmark normal UDP port publishing first; use host networking only if measured latency/socket-drop evidence warrants the portability/security tradeoff.
- Log structured lifecycle/error summaries to stdout/stderr. Export bounded counters/gauges through a standard-library endpoint (`expvar` or a small JSON metrics handler) before adding a monitoring client dependency.

## Anti-Patterns to Avoid

### Anti-Pattern 1: Per-Room Runtime Objects

**What:** A socket, goroutine, channel, timer, or actor per room.

**Why bad:** Idle rooms consume resources, shutdown becomes distributed, and cross-index cleanup becomes race-prone.

**Instead:** One socket, one store, one sweep; room state is data.

### Anti-Pattern 2: Network I/O While Holding State Lock

**What:** Fan out or marshal while a room/session mutex is held.

**Why bad:** One slow/erroring write stalls HTTP, expiry, binds, and unrelated rooms.

**Instead:** Validate and snapshot value-copy recipients under the lock, then release before encoding/writes.

### Anti-Pattern 3: Room ID or Source IP as Authentication

**What:** Accept data because the room ID exists or the packet came from an expected IP.

**Why bad:** Room IDs leak, many players share NAT IPs, source addresses can be spoofed, and mobile endpoints change.

**Instead:** Expiring random secret, address-validation challenge, random binding ID, and exact observed `AddrPort`.

### Anti-Pattern 4: Transparent Rebind on Any Data Packet

**What:** Update a session endpoint when a packet with its ID arrives from a new address.

**Why bad:** A guessed/captured ID can hijack or disconnect the real client.

**Instead:** Keep the old binding until a fresh authenticated challenge succeeds.

### Anti-Pattern 5: Unbounded Reliability or Backpressure

**What:** Retry UDP fan-out, queue until writable, or promise delivery/order.

**Why bad:** It converts packet loss into heap growth and head-of-line delay, violating the relay scope.

**Instead:** Best-effort bounded processing, drop counters, and client-owned reliability where needed.

### Anti-Pattern 6: Premature Distributed Seams

**What:** Repository interfaces, Redis keys, Kubernetes probes/resources, Agones/Open Match clients, or a generic allocator plugin system.

**Why bad:** None are exercised by the two committed milestones.

**Instead:** Stable HTTP allocation contract and concrete in-memory implementation; add distributed state only with a multi-instance requirement.

## Scalability Considerations

| Concern | ~100 concurrent sessions | ~10K concurrent sessions | ~1M concurrent sessions |
|---------|--------------------------|---------------------------|--------------------------|
| State | One mutex/maps; negligible | Measure lock hold time and heap; shard only if proven | Out of scope: allocate whole rooms to instances; external directory/control plane required |
| UDP receive | One read/dispatch loop | Benchmark socket drops; fixed readers/bounded workers only if needed | Many instances/regions and deliberate load balancing |
| Fan-out | Sequential writes | Participant cap and room/global circuit breaker dominate | Room affinity and capacity-aware allocation required |
| Expiry | One linear periodic sweep | Bucket deadlines or heap only if sweep cost is measured | Distributed ownership/reconciliation required |
| Deployment | One VM/container | Still possibly one tuned host; capacity test decides | Explicitly not this architecture |

The table identifies upgrade triggers, not v1 features. Do not implement the 10K/1M columns before measurement or scope change.

## Roadmap / Build Order Implications

1. **Wire contract and state kernel** — define v1 Protobuf, reproducible Go/C# generation, room/grant/binding state and deterministic unit/fuzz tests. Everything else depends on these invariants.
2. **Management control plane** — implement authenticated idempotent PUT/GET/DELETE, secret redaction, caps, and the future Director-to-CreateRoom HTTP seam.
3. **Authenticated UDP vertical slice** — one socket/loop, challenge-response, exact endpoint binding, 1200-byte cap, opaque two-client fan-out, and security integration tests. Include bounds/rate limits in this phase, not as later polish.
4. **Lifecycle and operability** — expiry sweep, consent/idle handling, safe rebind, empty-room cleanup, health/metrics, and deterministic signal drain/close.
5. **Unity native integration** — generated C# contract, background socket/main-thread queue, PC/mobile sample, pause/resume rebind and expired-allocation recovery.
6. **Performance evidence and tuning** — independent load generator, target workload definition, latency/loss/CPU/RSS/socket-drop report, race/fuzz run; add concurrency/socket tuning only from evidence.
7. **Single-host release** — CGO-free reproducible binary, systemd unit, minimal Docker image/Compose example, public-vs-listen endpoint configuration, stop/health verification and runbook.

Phases 1–6 deliver the first product milestone (correct single binary). Phase 7 validates the second (one VM or one Docker host). Packaging smoke checks may begin earlier, but operational acceptance belongs after the protocol and measured resource envelope are stable.

## Sources

All source-backed findings are **MEDIUM confidence** under the configured GSD confidence seam (`websearch --verified`), even where the linked material is a primary official source.

- [Go `net` package: PacketConn concurrency, UDP AddrPort APIs and deadlines](https://pkg.go.dev/net) — official, current as retrieved 2026-08-08.
- [Go `net/http.Server.Shutdown`](https://pkg.go.dev/net/http#Server.Shutdown) and [`os/signal.NotifyContext`](https://pkg.go.dev/os/signal#NotifyContext) — official lifecycle behavior.
- [Go `crypto/rand`](https://pkg.go.dev/crypto/rand), [`crypto/hmac`](https://pkg.go.dev/crypto/hmac), and [RFC 2104](https://www.rfc-editor.org/rfc/rfc2104.html) — random credentials and timing-safe HMAC verification.
- [RFC 8085: UDP Usage Guidelines](https://www.rfc-editor.org/rfc/rfc8085.html) — MTU, congestion, reliability, middlebox, keepalive and consent guidance.
- [RFC 4787: UDP NAT Behavioral Requirements](https://www.rfc-editor.org/rfc/rfc4787.html) and [RFC 7857 updates](https://www.rfc-editor.org/rfc/rfc7857.html) — mapping/filtering/timeout behavior.
- [RFC 9000, Section 8: Address Validation](https://www.rfc-editor.org/rfc/rfc9000.html#section-8) — anti-amplification and NAT-rebinding principles; used as a design analogy, not a claim of QUIC compliance.
- [RFC 7675: Consent Freshness](https://www.rfc-editor.org/rfc/rfc7675.html) — reason to stop UDP transmission to stale endpoints.
- [Protocol Buffers proto3 guide](https://protobuf.dev/programming-guides/proto3/), [C# generated code guide](https://protobuf.dev/reference/csharp/csharp-generated/), and [Buf breaking-change checks](https://buf.build/docs/breaking/) — cross-language evolution rules.
- [Unity `OnApplicationPause`](https://docs.unity3d.com/ScriptReference/MonoBehaviour.OnApplicationPause.html) and [`Application.quitting`](https://docs.unity3d.com/ScriptReference/Application-quitting.html) — native/mobile lifecycle behavior.
- [.NET Socket asynchronous receive](https://learn.microsoft.com/en-us/dotnet/api/system.net.sockets.socket.receiveasync) — cancellation-aware receive where supported; runtime compatibility still requires target validation.
- [Dockerfile reference: exec entrypoint, `STOPSIGNAL`, `HEALTHCHECK`](https://docs.docker.com/reference/dockerfile/) and [systemd.service manual](https://man7.org/linux/man-pages/man5/systemd.service.5.html) — single-host stop/restart semantics.
- [Go fuzzing](https://go.dev/doc/security/fuzz/) and [race detector](https://go.dev/doc/articles/race_detector) — built-in verification tools.

## Open Validation Gaps

- The 1200-byte default must be exercised on the actual PC/mobile carrier/VPN matrix; it is not a universal fragmentation guarantee.
- Unity socket cancellation/API availability differs by Unity scripting runtime and IL2CPP target, so close-to-cancel behavior needs native build tests.
- One UDP loop and one store mutex are the recommendation until the defined load harness supplies a measured saturation point; no capacity number is asserted by research alone.
