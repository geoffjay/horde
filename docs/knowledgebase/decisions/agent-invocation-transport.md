---
type: Decision
title: HTTP over unix domain sockets for agent invocation
description: Agent subprocesses expose a local HTTP API on a unix socket; the node reverse-proxies invoke requests to them.
tags: [decision, agents, transport, phase-3]
timestamp: 2026-07-10T00:00:00Z
---

# Context

Phase 3 connects the agent invocation seam left stubbed by Phase 2. The node
API's `POST /api/v1/agents/{id}/invoke` handler currently publishes a fake
`done` event to the bus (`internal/api/invoke.go`); the `horde agent`
subprocess constructs an ADK agent and discards it (`cmd/agent.go:46-55`).
Real invocation needs the node server to drive the agent subprocess and stream
events back to the SSE client.

Two constraints shape the choice:

* **Long-lived agents.** An agent subprocess stays alive across multiple
  invocations (spawn, list, stop is the lifecycle already shipped). The
  transport must support concurrent invocations to the same agent.
* **No new protocols.** Phase 2 established HTTP/JSON + SSE as the node API
  transport, with chi over `net/http` and an in-process event bus. Reusing
  that stack avoids designing a custom framing protocol.

See the [Phase 3 plan](/docs/knowledgebase/plans/phase-3-agents.md) for the
full scope.

# Options considered

## stdin/stdout NDJSON (duplex pipe multiplexing)

The server writes invocation requests to the agent subprocess's stdin as
newline-delimited JSON (`{invocation_id, message, ...}`) and reads
`{invocation_id, type, data}` event lines from stdout.

**Strengths:** no listener, no port, minimal surface area. The server already
owns the pipes.

**Weaknesses:** this is a custom bidirectional RPC protocol. Concurrent
invocations require request multiplexing on stdin and event demultiplexing
on stdout — interleaved lines tagged by `invocation_id`. Cancellation needs
a custom control message (`{type:"cancel", invocation_id}`). Backpressure
arises when the agent's stdout pipe fills and the server isn't reading
fast enough, with no standard framing to detect it. `Last-Event-ID` resume
has no equivalent without custom checkpointing in the protocol. Debugging
interleaved duplex pipes is painful and there is no `curl` equivalent.

This approach earns its simplicity only for single-invocation, one-shot
agents. For long-lived agents with concurrent invocations it re-invents an
RPC protocol with none of the tooling.

## HTTP over unix domain sockets

Each agent subprocess starts a local HTTP server on a unix domain socket
at a predictable path (`/tmp/horde-agent-{id}.sock`). The node's invoke
handler reverse-proxies the SSE stream from the agent's socket to the
client.

```
horde agent --name greeter --socket /tmp/horde-agent-{id}.sock
  GET  /health    → {"status":"ok"}
  POST /invoke     → text/event-stream (SSE)
```

The subprocess reports readiness by emitting a single NDJSON line on stdout
at startup (`{"type":"ready","socket":"/tmp/horde-agent-{id}.sock"}`); the
server reads this during `SpawnAgent` and records the socket path on the
`agentProc`. The node's `POST /api/v1/agents/{id}/invoke` looks up the
socket and uses `httputil.ReverseProxy` to pipe the SSE stream through to
the client.

**Strengths:**

* **No new protocol.** The wire format is HTTP/JSON + SSE — already
  specified in the [transport decision](http-api-transport.md) and
  implemented in Phase 2.
* **Concurrency is free.** HTTP handles multiple concurrent invocations on
  one agent without multiplexing logic.
* **Cancellation is free.** Client disconnect cancels the request context;
  the agent's `iter.Seq2` range loop breaks on context done. No custom
  cancel message.
* **`Last-Event-ID` resume works natively.** The reverse proxy passes the
  header through; the agent's SSE handler can replay buffered events from
  the last seen id.
* **Reuses Phase 2 code.** The agent subprocess reuses the chi router, the
  SSE handler pattern, the event types, and `internal/client` (HTTP client +
  SSE consumer). The node's invoke handler becomes a reverse proxy (~15
  lines replacing the current stub).
* **Debuggable.** `curl --unix-socket /tmp/horde-agent-{id}.sock
  POST /invoke` works.
* **No port conflicts.** Unix sockets are filesystem paths, not TCP ports.
  Hundreds of agents do not contend for port numbers.
* **Phase 4 reuse.** When a slave forwards an invocation to a remote agent,
  it is the same reverse-proxy pattern at a different transport level. The
  agent does not care whether its caller is local or remote.

**Weaknesses:**

* **No pipe-based liveness.** The server cannot detect a hung agent from a
  closed pipe. Mitigation: `cmd.Wait()` + `doneCh` already detect process
  death; a periodic `GET /health` poll on the socket detects a *hung*
  process (alive but not responding). The poll interval is a tuning
  parameter, not an architectural concern.
* **Per-agent listener overhead.** Each agent runs its own chi router and
  `http.Server`. At hundreds of agents per node this has overhead. If it
  becomes a problem, the same HTTP-over-socket code can be collapsed into a
  single multiplexed listener per node that routes by agent id — a local
  optimization that does not change the contract.
* **Socket file cleanup.** The subprocess must remove the socket file on
  graceful exit. The server should also clean up stale sockets on spawn
  failure. This is a small, well-understood concern.

## HTTP over TCP (loopback port per agent)

Same as unix sockets but using `127.0.0.1:{port}` per agent.

Rejected: reintroduces port allocation and port conflicts, no advantage over
unix sockets for a local-only transport.

# Decision

**HTTP over unix domain sockets.** Each agent subprocess serves a local
HTTP API (`GET /health`, `POST /invoke` with SSE response) on a unix socket.
The node server reverse-proxies `POST /api/v1/agents/{id}/invoke` to the
agent's socket, piping the SSE stream through to the client. The agent
subprocess reuses chi, the SSE handler pattern, and the event types from
Phase 2.

`Last-Event-ID` resume is honored: the agent's `/invoke` handler buffers
recent events and replays from the last seen id when the header is present.

# Consequences

* The agent subprocess becomes a mini-server, not a pipe reader. It needs
  its own chi router and SSE handler (reusable from `internal/api` patterns,
  but a separate package to avoid an import cycle — see the plan).
* `agentProc` gains a `socketPath` field, populated from the subprocess's
  stdout ready message.
* `invokeAgent` (`internal/api/invoke.go`) is rewritten as a reverse proxy
  to the agent's socket. The event bus is no longer involved in the
  invoke path (it remains for Phase 4 cross-node fan-out).
* `cmd/agent.go` is rewritten: it starts an HTTP server on the socket and
  serves the agent directly instead of blocking on `<-ctx.Done()`.
* A `GET /health` poll detects hung agents. The interval is configurable;
  the default is generous (e.g. 30s) since process death is already caught
  by `cmd.Wait()`.
* The agent subprocess owns its socket file lifecycle: create on start,
  remove on graceful exit.
* `Last-Event-ID` is honored at the agent's `/invoke` handler, not at the
  node's reverse proxy. The agent buffers recent events per invocation id
  and replays from the last seen id.
* LLM-backed agents are deferred to a later phase. Phase 3 agents are
  structurally real (streaming events, multi-turn context within one
  invocation) but do not call a model.
