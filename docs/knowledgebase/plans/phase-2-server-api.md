---
type: Plan
title: Phase 2 — Server API
description: Detailed plan for the node API transport, event streaming, and slave↔master contract.
tags: [plan, api, phase-2, server, networking]
timestamp: 2026-07-09T00:00:00Z
---

Phase 2 replaces the two stubs left by Phase 1 — `Server.Run` blocking on
`<-ctx.Done()` with no listener, and `connectLeader` unconditionally setting
`leaderOK = true` — with a real HTTP API transport and a real slave↔master
contract.

* Transport decision: [HTTP + SSE](/docs/knowledgebase/decisions/http-api-transport.md)
* TUI contract: [TUI consumes the node API](/docs/knowledgebase/decisions/tui-uses-node-api.md)
* Topology context: [master/slave model](/docs/knowledgebase/decisions/master-slave-model.md)

# Scope

Two logical channels, one transport (HTTP/JSON + SSE), one listener on
`server.port`:

| Channel       | Consumers              | Backed by                  |
|---------------|------------------------|----------------------------|
| Node control  | TUI, slaves, ops       | `Server` methods directly  |
| Event stream  | TUI, forwarding slaves | new in-process event bus → SSE |

All endpoints under `/api/v1`. JSON in/out unless noted. SSE for streaming.

# Node control

```
GET    /api/v1/node            node info: {mode, leader_connected, node_id, version}
GET    /api/v1/health          {status: "ok"}        — liveness, no deps
GET    /api/v1/ready           {status: "ready", leader: "ok|degraded"} — readiness
```

`/health` is a dumb "process is up" check. `/ready` distinguishes master
(always ready) from slave (ready when `LeaderConnected()` is true) — this is
what the existing `LeaderConnected()` and `Mode()` methods feed, and it is the
thing that replaces the fake `leaderOK = true` in `connectLeader`.

# Agents

Mirrors `Server.Agents()` / `SpawnAgent()` / (new) stop:

```
GET    /api/v1/agents          list: [{id, name, status}]
POST   /api/v1/agents          {name: "greeter"} → 201 {id, name, status}
GET    /api/v1/agents/{id}     one agent: {id, name, status}
DELETE /api/v1/agents/{id}     204 on stop, 404 if unknown
```

`SpawnAgent` already exists and returns an `id`; the API just wraps it.
`DELETE` needs a new `Server.StopAgent(id)` — currently only `Run`'s shutdown
path signals agents. `status` comes from `agentProc`: needs a `state` field
(`running` / `exiting` / `exited`) updated by the existing `doneCh` goroutine;
today `AgentInfo` only carries `ID` + `Name`.

# Agent invocation + event streaming

The greeter currently runs inside the `horde agent` subprocess and prints to
stdout. For the API to stream events it needs to forward them back. Two layers:

```
POST   /api/v1/agents/{id}:invoke   {message: "..."}  →  text/event-stream (SSE)
```

Response is an SSE stream of `session.Event`-shaped payloads:

```
event: token
data: {"author":"greeter","content":{"role":"model","parts":[{"text":"Hello..."}]}}

event: done
data: {"invocation_id":"..."}
```

Notes:

* `Last-Event-ID` header gives free resume on a dropped connection — valuable
  for long agent token streams that get interrupted. gRPC streaming has no
  equivalent without custom checkpointing.
* The backing store is the new in-process event bus (Go channels): the
  server's agent-process manager subscribes to the subprocess's stdout (once
  `horde agent` emits structured events instead of plain text) and fans events
  onto the bus; each SSE response handler subscribes to the bus filtered by
  invocation id.
* This is the seam where Phase 3 (real agents driven by the API) plugs in —
  the bus + SSE shape does not change, only what the subprocess emits.

# Event bus (internal, not an endpoint)

```go
type EventBus struct{ ... }
func (b *EventBus) Publish(invocationID string, ev Event)
func (b *EventBus) Subscribe(invocationID string) <-chan Event  // fan-out, buffered, drop-on-slow
```

Why it matters here (not Phase 4): SSE handlers need per-request cancellation
and per-invocation filtering, and the slave→master forwarder also subscribes
to forward events upstream. A channels-based in-process bus keeps all of that
uniform without a broker. Multiple SSE clients watching the same invocation
is just multiple subscribers.

See the [transport decision](/docs/knowledgebase/decisions/http-api-transport.md)
for why a brokerless messaging lib (ZeroMQ / nng) is deferred to Phase 4.

# Slave ↔ master

Replaces `connectLeader`'s `leaderOK = true` lie with real round-trips. Same
HTTP API — the master is just another node, so a slave reuses the same client
code:

```
POST   /api/v1/cluster/register     slave→master on connect:
                                 {node_id, mode:"slave", addr:"..."}
                                 → {ok, node_id, leader_id}
GET    /api/v1/cluster/heartbeat    slave→master every N s:
                                 {node_id, agents:[...]}
                                 → {ok, leader_id}
```

`connectLeader` becomes a real client: dial `s.cfg.Leader`, call
`/cluster/register`, set `leaderOK` from the response, then loop on
`/cluster/heartbeat`. Keep the 5s ticker + non-blocking, background contract.

**`TestStart_SlaveBecomesLeaderConnected` must change.** Today it constructs a
slave with `Leader: "master:13420"` and passes *only because* `connectLeader`
sets `leaderOK = true` unconditionally, with no network call. Once
`connectLeader` performs a real `/cluster/register`, that call cannot succeed
against a non-existent host, so `LeaderConnected()` stays false and the test
fails. The test must be reworked to register against an `httptest` master stub
(or the leader-client must be injectable so the test can supply a fake). This
is a test rewrite, not a contract to preserve — do not treat the current
unconditional-true behaviour as the spec.

# What needs to exist that does not yet

## `internal/server`

* `Server.StopAgent(id)` — signal one agent by id, mirroring the shutdown
  path's `Signal` + grace + `Kill`.
* `agentProc.state` field + an accessor surfacing it through `AgentInfo`.
* `EventBus` + `Subscribe` / `Publish` — new file
  `internal/server/eventbus.go`, or `internal/events` if it should be usable
  by the `horde agent` subprocess too.
* An HTTP mux + handlers — `internal/api` package (see "Layout" below).
  `Run` starts an `http.Server` and serves until ctx canceled, then shuts
  down. Existing agent teardown stays.
* Plumb the listen port + timeouts into `server.Config`. The port and
  read/write/idle timeouts exist on `config.ServerConfig` (in
  `internal/config/horde.go`) but are currently unused, and the server
  package's own `Config` struct (`internal/server/server.go`) carries only
  `Mode` / `AgentCommand` / `Leader` / `SpawnDefaultAgent` — no port, no
  timeouts. Those fields must be added to `server.Config` and populated from
  `config.ServerConfig` before `Run` can bind a listener.
* Slave client — `internal/server/leaderclient.go` (or `internal/api/client`),
  a thin HTTP client over `s.cfg.Leader`. `connectLeader` calls it.

## `agents/` (Phase 3 really, but the wire shape matters now)

* `horde agent` needs to emit structured events to stdout (newline-delimited
  JSON is simplest) instead of plain text, so the server's process manager can
  parse them onto the bus. For Phase 2 this can be stubbed — stream raw stdout
  as SSE `log` events to prove the pipe end-to-end.

## Config

Nothing new needed for Phase 2 — `server.port`, `server.leader`, and the three
timeouts are already defined and unused. `cluster.node_id` is already defined
for the register/heartbeat payloads.

# Layout

* `internal/api` — HTTP adapter: handlers, routes, request/response types.
  Calls into `internal/server`; never owns agent state itself. This keeps
  `internal/server` as the node core and makes the slave's leader-client
  reusable from the same package.
* `internal/server` — node core: the `Server`, `agentProc`, `EventBus`,
  `connectLeader`, `leaderclient`. Unchanged in role, gains the missing
  methods above.

# Open follow-ups (not blocking)

* ~~Stdlib `net/http` mux vs a thin router like `chi`~~ — **resolved:** chi
  adopted up front (see the [transport decision](/docs/knowledgebase/decisions/http-api-transport.md)
  for the chi-vs-fiber-vs-echo rationale).
* ~~TUI node startup mechanism~~ — **resolved:** the TUI does *not* start a
  node. It is a pure API client (`internal/client`) that probes
  `GET /api/v1/health` at the configured `host:port`; on failure it shows a
  60-second retry countdown (with an immediate-retry key) and never spawns
  a node in-process (see the [TUI decision](/docs/knowledgebase/decisions/tui-uses-node-api.md)).
* ~~`docs/environment.md` "does not yet expose a real API transport" note~~
  — **resolved:** the listener ships in Phase 2; the note is removed.
