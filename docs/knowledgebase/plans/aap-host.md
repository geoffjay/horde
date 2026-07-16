---
type: Plan
title: AAP host — driving external coding agents
description: The node spawns AAP adapters over the stdio binding, runs the initialize→ready→prompt→turn loop, wires tool approval to node policy, and consumes context/error/approval frames to populate the execution context at full fidelity.
tags: [plan, agents, aap, external-agents, phase-3.6]
timestamp: 2026-07-15T00:00:00Z
---

This plan lands the AAP *host* on the node: the code that spawns an external
agent adapter as a subprocess, drives the AAP v1 protocol with it, and bridges
AAP frames to horde's existing surfaces (invoke SSE, execution context,
approval authority). It turns horde from a host of toy ADK agents into a host
of real external coding agents.

* Protocol: [Agent Adapter Protocol v1](/docs/spec/agent-adapter-protocol-v1.md).
* Decision: [Adopt the Agent Adapter Protocol](/docs/knowledgebase/decisions/agent-adapter-protocol.md).
* Concept: [Agent Adapter Protocol (AAP)](/docs/knowledgebase/concepts/agent-adapter-protocol.md).
* Foundation already in place: `internal/aap` (typed messages, mock adapter,
  shared test vectors), the hidden `horde aap-mock` subcommand.
* Sibling mechanism: [Phase 3 — Agent mechanism](phase-3-agents.md) (native ADK
  agents over HTTP+SSE on a unix socket). AAP is a *separate* path; the two
  coexist at different seams.
* Observability already scaffolded: [Agent execution context](agent-execution-context.md)
  — its `applyStatus` / `applyContextUpdate` / `applyError` /
  `applyApprovalRequest` receivers in `internal/server/context.go` are wired
  and waiting (`nolint:unused`); this phase lights them up.

# Scope

**v1 delivers:**

1. An AAP host in `internal/server` that spawns an adapter over the stdio
   binding, performs the `initialize`→`ready` handshake, runs the prompt/turn
   loop, and shuts it down gracefully.
2. A second agent *kind* alongside native ADK: an agent whose subprocess is an
   AAP adapter rather than `horde agent`. Both kinds register in the same
   `agentProc` map and surface through the same API; the difference is the
   subprocess contract.
3. Bridging AAP turn output (`message`, `tool_call`, `turn_complete`) to the
   existing invoke SSE stream, so `POST /api/v1/agents/{id}/invoke` works
   unchanged against an AAP agent.
4. Consuming AAP `context` / `status` / `error` / `approval_request` frames to
   populate the execution context store at **full fidelity** — the signal
   source Slice A degrades without.
5. Node-as-sole-approval-authority: an `approval_request` from the adapter is
   surfaced as a pending approval in the execution context (visible in the
   TUI context pane); a decision is fed back to the adapter as
   `approval_response`. v1 wires the plumbing and an auto-approve/auto-deny
   policy; human approve/deny UI is a follow-up.
6. The project workspace mapped onto `initialize.workspace.cwd` and
   `initialize.permissions` (advisory scope, same `workspace` field Phase 3
   already passes through `--workspace`).
7. The `horde aap-mock` fixture driven end to end as the first adapter (it
   already ships; this phase uses it as the integration target).

**v1 does not deliver:**

* A real external adapter (e.g. Claude Code). The mock is the conformance
  target; a real adapter is a follow-up that depends only on this host landing.
* Human approve/deny *UI* in the TUI. The context pane already renders pending
  approvals (read-only); the decision endpoint and a keybinding are a follow-up
  that the plumbing here unblocks.
* The websocket binding. v1 is stdio-only (the mandatory baseline); websocket
  arrives when a real adapter needs it.
* Per-user / remote-principal authorization. The node decides approvals per
  its own policy; "whose prompt, whose approval" is 3.5b. The `permissions`
  scope is sent so a compliant adapter self-enforces; the node does not yet
  vary it per principal.
* AAP `resume_token`. Multi-turn context across invocations is already handled
  by the Phase 3.5 `(agent_id, project_id)` session key; AAP `resume_token` is
  an adapter-level resume that is additive and deferred.
* MCP server provisioning (`tools.mcp_servers`). The field exists in the
  `initialize` struct; v1 sends it empty. Provisioning MCP servers is a
  follow-up once a real adapter uses tools.

**Depends on:** Phase 3 (agent spawn/invoke mechanism) and Phase 3.5 Slice A
(execution context store). Both complete.

# The two agent kinds

Today every spawned agent is a native ADK agent: the node runs
`<binary> agent --name <name> --socket <path>` and reverse-proxies HTTP/SSE to
the subprocess's unix socket. Phase 3.6 adds a second kind:

| | Native ADK (Phase 3) | AAP adapter (this phase) |
|---|---|---|
| Subprocess | `horde agent` | a configured adapter command (argv) |
| Handshake | `spawn_ready` NDJSON on stdout (socket path) | AAP `initialize`→`ready` |
| Transport | HTTP/SSE on a unix socket (one-directional response) | NDJSON over stdio (full-duplex) |
| Turn loop | `runner.Run` → `session.Event` iterator | `prompt` → `message*`/`tool_call*`/`turn_complete` |
| Context signal | coarse (activity + errors, node-derived) | full (`context`/`status`/`error`/`approval_request` frames) |
| Approval | none (ADK has no tool-approval round-trip) | `approval_request`/`approval_response` |

Both kinds share: the `agentProc` registry, the execution context store, the
invoke API surface, project assignment, and the session-key derivation. The
node branches on the *kind* at spawn time and at the invoke bridge; everywhere
else an agent is an agent.

# Config

A new agent definition is configured rather than compiled in. Native ADK
agents (greeter, repeater) stay registry-built; AAP agents are *declared* in
config so an operator can add one without recompiling. This matches the
project's operator-controlled-config posture (see `server.go` G204 nolint:
`AgentCommand` is operator config, not untrusted input).

| Key | Default | Description |
| --- | --- | --- |
| `agents.<name>.kind` | `adk` | Agent kind: `adk` (registry-built, native) or `aap` (external adapter). |
| `agents.<name>.command` | *(empty)* | AAP only: the adapter command (argv[0]). |
| `agents.<name>.args` | `[]` | AAP only: adapter argv after the command. |
| `agents.<name>.env` | `{}` | AAP only: extra environment for the adapter (merged into the subprocess env; `AAP_TRANSPORT=stdio` is set by the host). |
| `agents.<name>.model` | *(empty)* | AAP only: requested model, passed as `initialize.model`. Empty → adapter default. |
| `agents.<name>.system_prompt` | *(empty)* | AAP only: a system prompt path or text for `initialize.system_prompt`. |
| `agents.<name>.permissions` | *(empty)* | AAP only: a `permissions` scope (`mode`, `writable_paths`, `deny_paths`) sent in `initialize`. Empty omits the scope (adapter self-enforces its own default). |
| `agents.<name>.auto_approve` | `false` | AAP only: when `tool_approval` is active, auto-approve every `approval_request` (v1 policy; a real approval-decision endpoint is a follow-up). When false and no decision path is wired, the request stays pending until the turn times out or is cancelled. |

This is a new `agents` map section under `Config` (in
`internal/config/horde.go`), keyed by agent name. A `kind: aap` entry makes
`agents/<name>` resolve to a configured AAP agent rather than the registry.
`agents.Get` stays the registry lookup for ADK; a new resolution path handles
AAP. `cmd/serve.go` maps the config into `server.Config` (as it does for the
existing `AgentCommand`, `ProjectWorkspaceDir`, etc.).

No new ports or services. AAP adapters are subprocesses, not listeners.

# Layer 1 — The AAP host session

The core new type is an AAP *host session*: the node-side state machine for one
adapter subprocess.

```
internal/server/aaphost.go

type aapHostSession struct {
    agentID   string
    name      string
    cmd       *exec.Cmd
    stdin     io.WriteCloser
    stdout    io.Reader       // buffered reader
    ready     *aap.Ready      // cached from handshake
    mu        sync.Mutex
    pending   map[string]chan aap.ApprovalDecision  // request_id → decision
}
```

Lifecycle, mirroring the spec §8:

1. **Spawn.** `exec.CommandContext` with the configured adapter argv, env
   (`AAP_TRANSPORT=stdio` + the legacy alias + configured extras), and piped
   stdin/stdout. stderr goes to the node's stderr (adapter logs are
   human-readable, never AAP frames). Reuse the G204 posture from
   `startAgentProcess`: the adapter command is operator config.
2. **Handshake.** Write `aap.Initialize` (protocol version, model, system
   prompt, workspace, permissions, empty tools) to stdin. Read the first agent
   frame; it must be `aap.Ready`. Validate `protocol_version`; cache
   `capabilities`. On a fatal `error` or a version mismatch, fail the spawn
   (mirror `readReadyHandshake`'s timeout path).
3. **Reader goroutine.** A single goroutine reads NDJSON lines from stdout,
   parses each as an `aap.AgentMessage`, and dispatches:
   - `message` / `tool_call` / `turn_complete` → forwarded to the active
     invoke stream (Layer 3).
   - `status` → `ctxStore.applyStatus(agentID, st)`.
   - `context` → `ctxStore.applyContextUpdate(agentID, cu)`.
   - `error` → `ctxStore.applyError(agentID, e)`; on `fatal`, mark the agent
     exited.
   - `log` → logrus at the matching level (adapter diagnostics).
   - `approval_request` → `ctxStore.applyApprovalRequest`; resolve per policy
     (Layer 4) and write `aap.ApprovalResponse` back.
   - Unknown type → log and skip (spec §3).
4. **Shutdown.** On stop: write `aap.Shutdown`, wait for the process to exit
   with the existing `agentShutdownGrace`, then SIGKILL if needed. This reuses
   `trackAgentExit`'s cleanup shape.

The reader goroutine owns stdout; the host writes to stdin (prompts, cancels,
approval responses, shutdown). Full-duplex, as the spec requires.

# Layer 2 — Spawning an AAP agent

`SpawnAgent` branches on the agent *kind*:

* **ADK** (default, current path): `spawnAgentWithWorkspace` unchanged —
  `horde agent` subprocess, `spawn_ready` handshake, unix socket.
* **AAP**: a new `spawnAAPAgent` that creates an `aapHostSession`, runs the
  initialize→ready handshake, and registers an `agentProc` with **no socket
  path** (AAP agents don't serve HTTP; the invoke bridge talks to the session,
  not a socket). `agentProc` gains a `kind` field and, for AAP, a back-pointer
  to the `aapHostSession`.

The `agents.Get` registry is the ADK path. A new resolution (config-driven)
returns an *agent definition* (kind + adapter config) rather than an ADK
`agent.Agent`. `SpawnAgent` takes the definition and dispatches. The
`agents.Register`/`Get` registry is untouched for ADK; AAP definitions live
in the server config built from the `agents.*` config section.

The ready-timeout and the health-polling story adapt:

* AAP ready is the `ready` frame, not `spawn_ready`; reuse the timeout constant.
* Health polling (`pollOneAgent`) currently hits `/health` on a unix socket.
  AAP agents have no HTTP endpoint, so for an AAP `agentProc` the poll is a
  no-op (or a lightweight "is the process alive" check). The reader goroutine
  already detects a fatal `error` / process exit; that replaces HTTP health
  for AAP.

# Layer 3 — Bridging AAP turns to the invoke SSE stream

Today `invokeAgent` (`internal/api/invoke.go`) reverse-proxies to the agent's
unix socket. For an AAP agent there is no socket to proxy to; the node itself
runs the turn and translates AAP frames to the SSE event shape the TUI and
other clients already consume.

```
POST /api/v1/agents/{id}/invoke   (unchanged URL, unchanged SSE response shape)
```

The invoke handler branches on `agentProc.kind`:

* **ADK**: the existing reverse proxy (unchanged).
* **AAP**: a new `invokeAAPAgent` that:
  1. Derives `session_id` from `(agent_id, project_id)` exactly as today
     (`srv.SessionKey`). This keeps multi-turn context per project unchanged.
  2. Generates a `turn_id` (the invocation id, reused).
  3. Writes `aap.Prompt{TurnID, Content: TextPrompt(message)}` to the session's
     stdin.
  4. Streams the reader goroutine's turn output to the client as SSE events
     in the existing shape (`token`/`done`/`error` events), translating AAP
     `message` → `token`, `turn_complete` → `done`, adapter `error`/fatal →
     `error`.
  5. Honours `Last-Event-ID` resume: the per-invocation ring buffer pattern
     from `internal/agentapi` is reused (the AAP turn writes to a buffer the
     HTTP reader tails), so a reconnecting client resumes from the buffer
     exactly as ADK invocations do.

The session-key derivation and the paused-project gate are unchanged — they
live above the kind branch. A paused project still returns 409; a finished
project still falls through to the no-project path.

This keeps the **invoke URL and SSE response shape unchanged**. The TUI's
invoke screen works against AAP agents without modification.

# Layer 4 — Tool approval (node as authority)

When the adapter advertises `tool_approval`, an `approval_request` arrives
mid-turn. The host:

1. Records it in the execution context via `applyApprovalRequest` (already
   implemented; the TUI context pane renders it read-only today).
2. Resolves a decision per policy:
   - **v1 (`auto_approve: true`):** immediately write
     `aap.ApprovalResponse{Decision: DecisionAllow}` and clear the pending
     ref via `applyApprovalResponse`.
   - **v1 (`auto_approve: false`, default):** the request stays pending. The
     turn continues to wait; the host does not auto-decide. A real
     decision endpoint (follow-up) writes the response. If the turn
     completes or is cancelled before a decision, the pending ref is
     cleared.
3. When the adapter lacks `tool_approval`: no round-trip; the `permissions`
   scope (if sent) is the only enforcement. The host logs that approval cannot
   be honoured for a restricted principal (spec §7), but v1 has no
   per-principal policy, so this is informational.

The decision endpoint (a new `POST /api/v1/agents/{id}/approvals/{requestID}`
with a `{decision}` body) is **scoped out of v1** to keep the phase focused on
the host mechanism. The `applyApprovalResponse` receiver already exists; the
endpoint is a thin handler once the UI keybinding lands. This phase leaves the
`auto_approve` config knob as the operative policy.

# Layer 5 — Execution context at full fidelity

This is where the `nolint:unused` receivers light up. The reader goroutine
calls:

| AAP frame | Receiver | Effect |
| --- | --- | --- |
| `status{state:busy}` | `applyStatus` | `Activity = busy/idle` (replaces the coarse ADK derivation) |
| `context{blocked,waiting_model,note,issue,...}` | `applyContextUpdate` | `Blocked`, `BlockedReason`, `WaitingModel`, `Note`, `Issue`, `TurnID` — the fields Slice A left empty |
| `error{code,message,fatal}` | `applyError` | bounded `Errors` slice |
| `approval_request` | `applyApprovalRequest` | `PendingApprovals` slice |
| `approval_response` (resolved) | `applyApprovalResponse` | removes the pending ref |

No new context-store code is needed — the receivers are implemented and
tested (`internal/server/context_test.go` already exercises them against
`aap.Status`/`ContextUpdate`/`Error`/`ApprovalRequest`). This phase wires the
call sites. The redaction (`Redacted()`) and digest
(`localContextDigests`) paths already carry these fields and need no change;
the cluster aggregation and the TUI context pane get richer data for free.

# Tests

* **Host session lifecycle:** spawn `horde aap-mock` (built binary) via the
  host, complete the handshake, send a prompt, assert the `message` +
  `turn_complete` frames arrive, shut down. Use the existing
  `bin/horde`-build-then-skip-in-CI pattern for subprocess tests.
* **Kind branch:** `SpawnAgent` for an ADK name vs a configured AAP name;
  assert the `agentProc.kind` and (for AAP) the empty socket path.
* **Invoke bridge:** `POST /agents/{id}/invoke` against an AAP agent; assert
  the SSE stream carries the mock's reply in the existing event shape.
  Reinvoke with the same session key and assert turn continuity where the
  adapter retains it (the mock is stateless; a stateful mock variant or a
  second prompt asserting a fresh turn is sufficient for v1).
* **Session-key derivation:** an AAP agent with an active project sends the
  project-derived session key through to the prompt path (the turn_id
  coexists; they are distinct, matching the decision doc).
* **Context fidelity:** drive the mock (extended to emit `context`/`status`/
  `error` frames) and assert the execution context snapshot reflects them.
  This may require a richer mock variant or a test-only adapter; the shared
  `internal/aap` package is the right home for a `RunFakeAdapter` that emits
  the full frame set.
* **Approval plumbing:** a mock variant that emits `approval_request`; with
  `auto_approve: true` assert the `approval_response` is sent and the pending
  ref clears; with `auto_approve: false` assert the pending ref stays until
  the turn ends.
* **Permissions scope:** an AAP agent config with a `permissions` scope; assert
  the `initialize` frame carries it (capture stdin in a test).
* **Graceful degradation:** an adapter that omits `execution_context` /
  `tool_approval` / `streaming` — assert the host degrades (no context
  frames, no approval round-trip, whole-message rendering) per spec §7.

# Open follow-ups (not blocking)

* **Real adapter.** The mock is the conformance target; a real adapter is a
  follow-up that depends only on the host landing. **pi-aap** (for the `pi`
  coding agent) is now wired and handshake-verified through the host — declared
  as an `agents.<name>.kind: aap` entry, spawned over stdio, and exercised by
  `TestSpawnAAPAgent_PiAdapter` (opt-in via `HORDE_TEST_PI_ADAPTER`); a live
  turn against a model is verified manually. See
  [`docs/examples/pi-agent.yaml`](/docs/examples/pi-agent.yaml). A Claude Code
  adapter remains possible against the schema-compatible agentd protocol
  (`agentd-adapter-claude` reference).
* **Human approve/deny UI.** ✅ **Delivered** (follow-up after 3.6). The
  `POST /api/v1/agents/{id}/approvals/{requestID}` endpoint (`{"decision":
  "allow"|"deny"}`) resolves a pending approval through `Server.RespondApproval`
  → the host session; the TUI agent view selects a pending approval (`↑↓`) and
  decides with `a`/`d`. With `auto_approve: false` a request stays pending until
  a human decides. This closes the [TUI plan's deferred
  approval-decision](tui-projects.md) item.
* **Websocket binding.** stdio is the mandatory baseline; websocket arrives
  when a real adapter needs a non-subprocess transport.
* **AAP `resume_token`.** Adapter-level resume is additive over the
  `(agent_id, project_id)` session key and deferred.
* **MCP server provisioning.** `initialize.tools.mcp_servers` is sent empty
  in v1; provisioning is a follow-up once a real adapter uses tools.
* **Per-principal `permissions`.** Varying the scope by who is invoking is
  3.5b; v1 sends one configured scope per agent.
