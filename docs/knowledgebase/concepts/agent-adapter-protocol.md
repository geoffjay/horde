---
type: Concept
title: Agent Adapter Protocol (AAP)
description: The vendor-neutral NDJSON protocol between a horde node (host) and an external AI coding agent driven through an adapter process.
tags: [concept, agents, protocol, aap]
timestamp: 2026-07-11T00:00:00Z
---

# What AAP is

The **Agent Adapter Protocol (AAP)** is the vendor-neutral wire protocol horde
uses to drive **external AI coding agents**. A host (a horde node) launches an
**adapter** — a process that translates AAP to and from a specific agent's
native format — and exchanges NDJSON frames with it: one JSON object per line,
discriminated by a top-level `type` field.

The canonical spec is [`docs/spec/agent-adapter-protocol-v1.md`](/docs/spec/agent-adapter-protocol-v1.md).
The decision to adopt and own it is
[Adopt the Agent Adapter Protocol](/docs/knowledgebase/decisions/agent-adapter-protocol.md).

AAP is distinct from the [ADK agent model](/docs/knowledgebase/concepts/agent-model.md):
ADK agents are horde-native, built in-process on `google.golang.org/adk/v2` and
invoked over HTTP/SSE on a unix socket (Phase 3). AAP agents are *external*
programs wrapped by an adapter, driven interactively over a full-duplex binding.
Both are "agents" to a node; they use different transports and suit different
jobs.

# Shape of the protocol

* **Handshake.** Host sends `initialize` (all config: model, system prompt,
  workspace, MCP tools, permission scope, resume token). Adapter replies with
  `ready`, advertising its `capabilities`.
* **Turns.** Host sends `prompt` (`turn_id`); adapter streams `message` /
  `tool_call` frames, optionally `approval_request`, and closes with
  `turn_complete` (usage/cost). `status` reports busy/idle; `log` carries
  diagnostics.
* **Control.** Host may send `cancel`, `clear_context`, `shutdown`, and
  `approval_response`. Because control frames flow mid-turn, every binding is
  **full-duplex**.
* **Capabilities.** Adapters advertise tokens (`streaming`, `thinking`,
  `tool_approval`, `usage_reporting`, `cost_reporting`, `context_clear`,
  `cancel`, `mcp`, `system_prompt_append`, `resume`, `permissions`,
  `execution_context`); the host
  degrades gracefully when one is absent.

# Bindings

The message schema is identical across bindings; only the byte transport
differs.

* **stdio** (mandatory): frames on the adapter's stdin/stdout, logs on stderr.
  Selected by `AAP_TRANSPORT=stdio`.
* **websocket** (optional): the adapter dials back to `AAP_WS_URL`.
* **Not a binding:** HTTP+SSE — it is one-directional and cannot carry
  host→agent control frames.

Env vars use the canonical `AAP_*` names; the legacy `AGENTD_AAP_*` names are
accepted as deprecated aliases.

# Interop and permissions

The v1 schema is a compatible superset of agentd's original protocol, so the
same adapter runs under horde or agentd. AAP's one horde addition is the
optional `initialize.permissions` scope (`read_only` / `read_write` plus
writable/deny paths), which a compliant adapter self-enforces — belt-and-braces
with the host's tool-approval authority.

AAP itself carries **no** notion of remote users or nodes. Multi-user,
cross-node, and remote-principal authorization live in horde's node +
[cluster layer](/docs/knowledgebase/decisions/master-slave-model.md) above AAP;
the node enforces write-gating at the AAP boundary by being the sole approval
authority.

# In the codebase

* `internal/aap` — typed `HostMessage`/`AgentMessage`, (de)serialization,
  transport env resolution (`TransportFromEnv`), and `RunMockAdapter`.
* `internal/aap/testdata/vectors.json` — the shared wire test vectors.
* `cmd/aapmock.go` — the hidden `horde aap-mock` subcommand (conformance
  fixture / reference adapter).
* `internal/server/aaphost.go` — the node-side AAP host session: adapter
  subprocess lifecycle, the `initialize`→`ready` handshake, the reader
  goroutine that dispatches every agent frame, approval resolution, and
  graceful shutdown (Phase 3.6).
* `internal/server/aapinvoke.go` — bridges an AAP turn to the invoke SSE
  stream shape, with a per-invocation ring buffer for `Last-Event-ID` resume.
* AAP agents are a second agent *kind* alongside native ADK, declared in
  config (`agents.<name>.kind: aap`) and sharing the `agentProc` map, the
  invoke API, and project assignment.

# Real adapters

The mock (`horde aap-mock`) is the conformance fixture. The first real external
adapter is **pi-aap** — a TypeScript adapter for the `pi` coding agent, in a
separate repository. It implements the full AAP v1 stdio path (handshake, turn
loop, tool-approval round-trip, MCP provisioning, execution-context frames) and
passes horde's shared wire vectors.

Wiring a real adapter is operator config, not code — declare it under
`agents.<name>` with `kind: aap` and point `command`/`args` at the adapter
binary. A worked example is [`docs/examples/pi-agent.yaml`](/docs/examples/pi-agent.yaml).
The host passes its own environment plus any configured `env` entries to the
adapter subprocess, so a provider key (e.g. `ANTHROPIC_API_KEY`) reaches the
adapter without being hard-coded in config.

Verification has two tiers:

* **Handshake** (no credentials, no network): the host spawns the adapter and
  completes `initialize`→`ready`. Covered by `TestSpawnAAPAgent_PiAdapter` in
  `internal/server`, opt-in via `HORDE_TEST_PI_ADAPTER=<path to the adapter's
  built entry point>` (skipped otherwise so CI stays green without the external
  repo). This asserts the host drives a real adapter, not just the mock.
* **Live turn** (needs a provider key + network): configure the agent as above
  and invoke it through `POST /api/v1/agents/{id}/invoke`. This exercises the
  full turn loop against the real model and is verified manually.
