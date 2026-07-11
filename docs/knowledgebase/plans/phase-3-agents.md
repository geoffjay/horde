---
type: Plan
title: Phase 3 — Agent mechanism
description: Long-lived agent subprocesses invoked over HTTP on unix sockets, with streaming, resume, and a real agent registry.
tags: [plan, agents, phase-3, transport]
timestamp: 2026-07-10T00:00:00Z
---

Phase 3 connects the invocation seam left stubbed by Phase 2. The node API's
`POST /api/v1/agents/{id}/invoke` handler currently publishes a fake `done`
event (`internal/api/invoke.go:54-58`); the `horde agent` subprocess constructs
an ADK agent and discards it (`cmd/agent.go:46-55`). Phase 3 makes the
subprocess serve the agent and the node proxy invocations to it.

* Transport decision: [HTTP over unix domain sockets for agent invocation](/docs/knowledgebase/decisions/agent-invocation-transport.md)
* Node API transport: [HTTP + SSE](/docs/knowledgebase/decisions/http-api-transport.md)
* Topology context: [master/slave model](/docs/knowledgebase/decisions/master-slave-model.md)

# Scope boundary

Phase 3 delivers the **mechanism**: the transport, the invocation loop,
streaming, resume, and a real agent registry. It does **not** introduce
users, projects, teams, permissions, or LLM-backed agents. Those are a
separate phase (see the roadmap) that builds on the mechanism delivered here.

# Agent subprocess as a mini-server

Each agent subprocess starts an HTTP server on a unix domain socket and
serves two endpoints:

```
horde agent --name <name> --socket /tmp/horde-agent-{id}.sock

GET  /health    → {"status":"ok"}
POST /invoke    → text/event-stream (SSE)
```

The `POST /invoke` request body:

```json
{"message": "Hello", "invocation_id": "uuid-optional"}
```

When `invocation_id` is omitted the agent generates one and includes it in
the first SSE event. The response is an SSE stream of `session.Event`-shaped
payloads:

```
event: invocation
data: {"invocation_id":"...","agent_id":"..."}

event: token
data: {"author":"greeter","content":{"role":"model","parts":[{"text":"Hello..."}]}}

event: done
data: {"invocation_id":"..."}
```

## Running the agent: use `runner.Runner`, not `agent.Run` directly

The agent is **not** run by calling `a.Run(...)` directly. `agent.Run` takes
an `agent.InvocationContext`, and that interface has **no public
constructor** — in ADK v2 it is built only by the module-internal package
`google.golang.org/adk/v2/internal/context`, which Go's internal-package
rule forbids importing from horde. The only public entry point that runs an
agent is `runner.Runner.Run`, which builds the `InvocationContext` for us and
requires a `session.Service`.

The agent subprocess constructs one `runner.Runner` at startup:

```go
r, err := runner.New(runner.Config{
    AppName:           "horde",
    Agent:             a,                        // from agents.Get(name)
    SessionService:    session.InMemoryService(), // session/service.go
    AutoCreateSession: true,
})
```

The `/invoke` handler then runs the agent and ranges the returned iterator:

```go
msg := &genai.Content{Role: genai.RoleUser, Parts: []*genai.Part{{Text: req.Message}}}
events := r.Run(r.Context(), userID, sessionID, msg, agent.RunConfig{
    StreamingMode: agent.StreamingModeSSE, // confirm the exact enum name
})
for ev, err := range events {
    if err != nil { /* write event: error, break */ }
    writeSSE(w, flusher, "token", ev) // assign the SSE id: field (see resume)
}
// range complete → write event: done
```

The runner keys conversations on `userID` + `sessionID`. For Phase 3 use a
fixed `userID` (e.g. `"local"`) and derive `sessionID` from the invocation
id, so each invocation is its own session. This is also what makes the
"multi-turn context across invocations" follow-up cheap later: reusing a
`sessionID` across `/invoke` calls gives session-scoped history for free from
the `SessionService` — no new mechanism needed (see follow-ups).

If the client disconnects, the handler stops writing but **must not** cancel
the underlying run when resume is enabled — see below.

## `Last-Event-ID` resume

Resume requires decoupling the agent run from the HTTP request lifecycle. A
naive "range the iterator directly onto the ResponseWriter, break on
disconnect" loop **cannot** support resume: if a disconnect cancels the run
there is nothing left to reconnect to, and re-entering `runner.Run` on
reconnect would re-execute the agent (duplicate output, repeated side
effects). So Phase 3 uses a small per-invocation broker:

1. On a **new** invocation (no `invocation_id`, or an id the agent has not
   seen), the handler starts the `runner.Run` loop in a **background
   goroutine** whose lifetime is tied to the invocation, not the HTTP
   request. That goroutine appends every event — with a monotonic sequential
   id — into a bounded per-invocation ring buffer (e.g. 256 events) and
   notifies any attached readers.
2. The HTTP handler is always a **reader/tailer**: it replays buffered
   events with id greater than `Last-Event-ID` (0 if the header is absent),
   then blocks tailing new events until `done` or client disconnect.
3. On **reconnect** (a known `invocation_id` + `Last-Event-ID`), the handler
   attaches as a new reader to the still-running (or already-finished)
   invocation and replays from the buffer — it does **not** call
   `runner.Run` again.
4. Client disconnect detaches the reader; it does **not** cancel the
   background run. The run completes and its events stay in the buffer for a
   later reconnect. (A node-side timeout to reap abandoned invocations is a
   follow-up, listed below.)

Each SSE event carries its buffer id in the `id:` field. Old invocations are
evicted from the buffer (bounded count or TTL). For Phase 3 the buffer is
in-process memory in the agent subprocess. A persisted store (for cross-node
resume in Phase 4) is a follow-up.

> **Scope cut option.** If full reattach-after-disconnect is more than Phase
> 3 needs, keep the ring buffer + `id:` fields (cheap, forward-compatible)
> but run the iterator directly on the request and defer the background
> broker + live reattach to Phase 4. Pick one explicitly; do not ship the
> contradiction of "cancel on disconnect" plus "replay after disconnect."

# Ready handshake

The subprocess emits a single NDJSON line on stdout at startup:

```json
{"type":"ready","socket":"/tmp/horde-agent-{id}.sock"}
```

The server reads this during `SpawnAgent` (scanning the first stdout line
before recording the proc). The socket path is stored on `agentProc`. If no
ready line arrives within a timeout (e.g. 5s), spawn fails.

The subprocess owns the socket file lifecycle: it creates the socket on
start and removes it on graceful exit. The server cleans up stale sockets
on spawn failure.

# Node server: reverse proxy

`invokeAgent` (`internal/api/invoke.go`) is rewritten. Instead of
publishing a fake `done` event to the bus, it:

1. Looks up the agent proc by id.
2. Reads the `socketPath`.
3. Creates an `httputil.ReverseProxy` with a transport that dials the unix
   socket.
4. Proxies the request, piping the SSE stream through to the client.

The event bus is **no longer involved in the invoke path**. It remains
available for Phase 4 cross-node event fan-out, but the direct
proxy-to-socket path is simpler and lower-latency. The `bus` parameter
is removed from `invokeAgent`'s signature.

```go
func invokeAgent(srv agentView) http.HandlerFunc {
    return func(w http.ResponseWriter, r *http.Request) {
        id := chi.URLParam(r, "id")
        socketPath := srv.AgentSocket(id) // new method
        if socketPath == "" {
            http.Error(w, `{"error":"agent not found"}`, http.StatusNotFound)
            return
        }
        proxy := httputil.NewSingleHostReverseProxy(&url.URL{
            Scheme: "http",
            Host:   "unix", // overridden by the dialer
        })
        proxy.Transport = &http.Transport{
            DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
                return net.Dial("unix", socketPath)
            },
        }
        proxy.FlushInterval = -1 // flush every write; defensive for SSE
        proxy.ServeHTTP(w, r)
    }
}
```

On Go 1.26 `httputil.ReverseProxy` already auto-flushes responses with a
`text/event-stream` content type, so the stream would pass through even
without `FlushInterval`; setting it to `-1` makes the streaming intent
explicit and independent of that heuristic. Note that
`NewSingleHostReverseProxy` leaves the client's `Host` header intact — this
is harmless over the unix socket.

The `agentView` interface gains an `AgentSocket(id string) string` method.

# Agent registry

`agents/` gains a registry so `--name` selects a real agent. Today
`agents.New()` ignores the name and always returns greeter
(`agents/agents.go:23`).

```go
// agents/registry.go
package agents

// Register adds an agent factory under the given name.
func Register(name string, fn func() (agent.Agent, error))

// Get returns the agent for the given name, or an error if unknown.
func Get(name string) (agent.Agent, error)

// Names returns all registered agent names.
func Names() []string
```

Agents register themselves via `init()`:

```go
// agents/greeter.go
func init() {
    Register("greeter", func() (agent.Agent, error) {
        return agent.New(agent.Config{
            Name:        "greeter",
            Description: "A hello-world agent that greets the user.",
            Run:         runGreeter,
        })
    })
}
```

`agents.go` becomes `agents/greeter.go` (the greeter implementation) plus
`registry.go` (the registry). `cmd/agent.go` calls `agents.Get(agentName)`
instead of `agents.New()`.

The greeter remains non-LLM — it echoes and streams. A second structurally
real agent should be added to prove the registry works end-to-end and to
exercise multi-turn context within one invocation (e.g., a "repeater" that
counts turns). Both are non-LLM; LLM-backed agents are deferred.

# `cmd/agent.go` rewrite

The current `runAgent` constructs the agent, discards it, and blocks on
`<-ctx.Done()`. The rewrite:

1. Parse `--name` and `--socket` flags.
2. Call `agents.Get(name)` — fail on unknown agent.
3. Build a `runner.Runner` (see "Running the agent" above): `runner.New`
   with the agent, `session.InMemoryService()`, and `AutoCreateSession`.
4. Start an HTTP server on the unix socket (`net.Listen("unix", socketPath)`).
5. Wire a chi router with `GET /health` and `POST /invoke` (SSE), passing the
   runner into the handlers (`internal/agentapi`).
6. Emit the ready handshake on stdout: `{"type":"ready","socket":"..."}`.
7. Block on `<-ctx.Done()`, then shut down the HTTP server and remove the
   socket file.

The `--socket` flag is passed by the server during `SpawnAgent`. If empty,
the agent generates a path (`os.TempDir()` + `horde-agent-{pid}.sock`).

Because the ready handshake is a single NDJSON line on **stdout**, the agent
must keep stdout clean: logrus already writes to stderr
(`setupLogging` → `logrus.SetOutput(os.Stderr)` in `cmd/tui.go`), so preserve
that and emit nothing else on stdout after the ready line.

# `internal/server` changes

## `agentProc`

Gains a `socketPath` field:

```go
type agentProc struct {
    id         string
    name       string
    state      AgentState
    cmd        *exec.Cmd
    doneCh     chan struct{}
    socketPath string // populated from the subprocess ready handshake
}
```

## `SpawnAgent`

Changes:

1. Accept the socket path as a parameter (or generate one).
2. Pass `--socket <path>` to the agent subprocess.
3. Scan the subprocess stdout for the first NDJSON line, parse the ready
   message, and record `socketPath` on the `agentProc`.
4. After the ready line, pipe remaining stdout to `os.Stdout` as before
   (or log it — the agent should not emit on stdout after ready; any
   further output is unexpected).
5. Set a timeout (e.g. 5s) for the ready handshake. If no ready line
   arrives, kill the process and return an error.
6. Replace the current name-ignoring verification. `SpawnAgent` today calls
   `agents.New()` (`server.go:252`), which always builds greeter regardless
   of `name`. Switch this to `agents.Get(name)` so an unknown agent fails
   before the subprocess is spawned.

`cmd.Stdout` is replaced with a scanner that reads the first line and then
switches to `io.Discard` (or a pipe to `os.Stderr` for diagnostics).

## New methods

```go
// AgentSocket returns the unix socket path for the given agent id, or
// "" if the agent is unknown or not yet ready.
func (s *Server) AgentSocket(id string) string

// IsAgentReady reports whether the agent subprocess has completed its
// ready handshake. Used by a health poll (see below).
func (s *Server) IsAgentReady(id string) bool
```

## Health polling (hung-agent detection)

A background goroutine polls each agent's `GET /health` at a configurable
interval (default 30s). If an agent fails to respond within a timeout
(e.g. 5s), it is marked unhealthy in `AgentInfo` (a new field or a new
state). The poll uses the agent's unix socket.

This detects hung processes that `cmd.Wait()` cannot. Process death is
already handled by `cmd.Wait()` + `doneCh`.

# `internal/api` changes

## `invoke.go`

Rewritten as a reverse proxy (see above). The `bus *server.EventBus`
parameter is removed from `invokeAgent` and from `Router`.

## `router.go`

`Router` signature drops the `bus` parameter (or keeps it for future use
with `nil` — cleanest to remove it and add it back in Phase 4 if needed):

```go
func Router(srv *server.Server) http.Handler
```

Update every call site, not just `cmd/serve.go`:
`internal/server/integration_test.go` and the `internal/api` handler tests
also construct the router via `Router(srv, bus)` and must drop the `bus`
argument. `NewInvocationID` (currently in `internal/server/eventbus.go`) is
still used — the agent generates the invocation id — so it stays even though
the bus leaves the invoke path.

## `types.go`

`agentView` gains `AgentSocket(id string) string`:

```go
type agentView interface {
    Agents() []server.AgentInfo
    SpawnAgent(ctx context.Context, name string) (string, error)
    StopAgent(id string) error
    AgentSocket(id string) string
}
```

# Config

New keys:

| Key | Default | Env var | Description |
|-----|---------|---------|-------------|
| `agent.socket_dir` | `/tmp` | `HORDE_AGENT_SOCKET_DIR` | Directory for agent unix socket files. |
| `agent.ready_timeout` | `5` | `HORDE_AGENT_READY_TIMEOUT` | Seconds to wait for an agent subprocess ready handshake. |
| `agent.health_poll_interval` | `30` | `HORDE_AGENT_HEALTH_POLL_INTERVAL` | Seconds between agent health polls. |

These should be added to `internal/config/horde.go` and documented in
`docs/environment.md` and [Environment](/docs/knowledgebase/concepts/environment.md).

# Layout

| Package | Role |
|---------|------|
| `agents/` | Agent definitions + registry. One file per agent (`greeter.go`, `repeater.go`) plus `registry.go`. |
| `internal/agentapi` | HTTP handlers for the agent subprocess (`/health`, `/invoke`). Drives a `runner.Runner` (built over the agent + `session.InMemoryService()`) and owns the per-invocation ring buffer / broker for resume. Separate from `internal/api` to avoid an import cycle (`internal/agentapi` imports `agents`; `internal/api` imports `internal/server`). Reuses the chi router and SSE write pattern from `internal/api`. |
| `internal/server` | Node core: `SpawnAgent` with ready handshake + `agents.Get(name)` verification, `AgentSocket`, health polling, `agentProc.socketPath`. |
| `internal/api` | Node API: `invokeAgent` rewritten as reverse proxy. `Router` drops `bus` param. |
| `cmd/agent.go` | Builds the `runner.Runner`, wires `agents.Get` + `internal/agentapi` into an HTTP server on the socket. |

# Tests

* **Agent registry:** unit test `Register`/`Get`/`Names`, unknown name
  returns error.
* **Agent subprocess `/invoke`:** `httptest` against the agent's router with
  a real `agents.Get("greeter")` — verify SSE events stream correctly and
  `done` is emitted.
* **`Last-Event-ID` resume:** start an invocation, disconnect the reader
  mid-stream, then reconnect with the same `invocation_id` + `Last-Event-ID`;
  verify the replayed buffered events precede any new ones and that
  `runner.Run` was entered exactly once (the reconnect must not re-run the
  agent).
* **Disconnect does not cancel the run:** start an invocation, disconnect the
  reader before `done`, then reconnect and confirm the full event sequence
  (including `done`) is still available from the buffer.
* **`SpawnAgent` ready handshake:** test that `SpawnAgent` records the
  socket path from the subprocess ready line. Use `SpawnDefaultAgent: false`
  and a fake `AgentCommand` (a small helper binary or `os.Executable()` with
  test-only args — follow the existing pattern in `server_test.go`).
* **Reverse proxy invoke:** integration test — spawn an agent, `POST
  /api/v1/agents/{id}/invoke` via `httptest`, verify the SSE stream passes
  through. This is the end-to-end Phase 3 test.
* **Hung-agent health poll:** test that an agent that stops responding to
  `/health` is marked unhealthy. Use a fake socket or a test agent that
  hangs on `/health`.
* **Socket cleanup:** test that the socket file is removed on graceful exit
  and that stale sockets are cleaned up on spawn failure.

# Open follow-ups (not blocking)

* **LLM-backed agents:** deferred. The mechanism delivered here is
  transport-agnostic to the agent's internals; an LLM-backed agent is a new
  `agents/llm.go` that calls `genai.Client`. The invocation contract does
  not change.
* **Multi-turn context across invocations:** Phase 3 delivers multi-turn
  context *within* a single invocation. Because the agent already runs
  through a `runner.Runner` backed by a `SessionService`, reusing a
  `sessionID` across `/invoke` calls would give conversation state for free —
  no new mechanism, just a stable session key. Exposing that (mapping a
  client conversation to a `sessionID`) depends on the project/session
  concept and is deferred to the multi-agent context phase.
* **Per-invocation cancellation from the node:** the reverse proxy passes
  client disconnect through naturally. Note the agent subprocess intentionally
  does **not** cancel the run on reader disconnect (so resume works), so a
  node-side timeout — plus a reaper for abandoned/finished invocations left in
  the ring buffer — is a follow-up, not blocking.
* **Agent-to-agent messaging:** deferred to the multi-agent context phase.
