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
* `internal/aap/session.go` — `HostSession`, the host-side driver: it spawns
  nothing itself but, given an adapter's stdio, runs the lifecycle
  (`Initialize` → ready, `Prompt` → turn with streamed frames + approval
  round-trip, `Shutdown`).
* `internal/aap/testdata/vectors.json` — the shared wire test vectors.
* `cmd/aapmock.go` — the hidden `horde aap-mock` subcommand (conformance
  fixture / reference *adapter*).
* `cmd/aaprun.go` — the hidden `horde aap-run` subcommand: the *host* side. It
  resolves an adapter (from `--command` or the `adapters` config section),
  spawns it with `AAP_TRANSPORT=stdio`, and drives one turn via `HostSession`.
  External adapters are configured under `adapters.<name>`
  (`command`/`args`/`env`/`model`) — distinct from the ADK `agent_command`.
