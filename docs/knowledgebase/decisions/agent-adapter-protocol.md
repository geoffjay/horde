---
type: Decision
title: Adopt the Agent Adapter Protocol (AAP) for external coding agents
description: horde owns a vendor-neutral NDJSON host↔adapter protocol, schema-compatible with agentd, to drive external AI coding agents; permission/multi-user concerns stay above it.
tags: [decision, agents, protocol, transport, aap]
timestamp: 2026-07-11T00:00:00Z
---

# Context

horde is the successor to agentd, an orchestrator of AI coding agents. horde
must drive **external coding agents** (Claude Code and others), not only the
native in-process ADK agents of Phase 3, and horde agents must be
**interoperable** with agentd's agents.

agentd already defined a working protocol for this — the "agentd Agent Protocol"
(`~/Projects/agentd/docs/spec/agent-protocol-v1.md`, crate
`~/Projects/agentd/crates/agent-protocol`): a vendor-neutral NDJSON contract
between a host and an adapter that wraps a specific agent. The question was
whether to align with it, invent a horde-native protocol, or something between.

Two things clarified the choice:

* **Protocol vs. transport are separate layers.** The interoperability surface
  is the *message schema* (`type`-tagged `HostMessage`/`AgentMessage` frames),
  not the byte transport. agentd's own spec states the schema is identical
  across bindings.
* **AAP is bidirectional; SSE is not.** Mid-turn the host sends `cancel` /
  `approval_response` and the agent sends `approval_request`. That needs a
  full-duplex binding. horde's [HTTP + SSE decision](http-api-transport.md)
  and [unix-socket agent invocation](agent-invocation-transport.md) were scoped
  to non-interactive ADK invoke and are **not** an AAP binding.

# Decision

Adopt and **own** the protocol in horde under the vendor-neutral name **Agent
Adapter Protocol (AAP)** — the acronym is preserved. The canonical spec lives
here: [`docs/spec/agent-adapter-protocol-v1.md`](/docs/spec/agent-adapter-protocol-v1.md).

* **Schema compatibility.** AAP v1's message schema is a compatible superset of
  agentd's original v1 — same `type` tags and field semantics. It evolves
  **additively only** (new optional fields, message types, capability tokens).
  This gives interop with existing agentd adapters with near-zero change to
  agentd.
* **Bindings.** stdio (mandatory, full-duplex — horde already emits the Phase 3
  ready handshake as NDJSON on stdout) and websocket (optional). HTTP+SSE is
  explicitly **not** an AAP binding.
* **Env vars.** Canonical `AAP_TRANSPORT` / `AAP_WS_URL`; adapters accept the
  legacy `AGENTD_AAP_*` names as deprecated aliases (canonical wins when both
  are set).
* **Permissions.** AAP v1 adds one thing agentd did not have: an optional
  `initialize.permissions` scope (capability `permissions`) that a compliant
  adapter self-enforces, for restrictive-by-default handling of agents driven on
  behalf of a remote principal. This is additive and optional.
* **Layering.** AAP is the *local* one-host-to-one-adapter contract. Multi-user,
  cross-node, remote-principal authorization, directory sync, and "who may send
  a mutating prompt" all live in the node-authorization + cluster layer **above**
  AAP (see [master/slave model](master-slave-model.md)). The node is the sole
  tool-approval authority, so it enforces write-gating at the AAP boundary.

# Consequences

**Positive**

* Third parties integrate any agent by implementing one documented protocol; the
  same adapter runs under horde or agentd unchanged.
* The `permissions` scope gives defense-in-depth that does not depend on the
  `tool_approval` capability round-trip.
* AAP and the Phase 3 ADK/HTTP path coexist cleanly at different seams: ADK
  agents over HTTP/SSE on a unix socket (native, non-interactive); AAP adapters
  over stdio/websocket (external, interactive).

**Negative / trade-offs**

* horde now owns a spec another project (agentd) depends on; changes must stay
  additive to preserve interop, or bump `protocol_version`.
* A thin adapter process sits in the launch path for each external agent.

# Implementation

* **Spec:** [`docs/spec/agent-adapter-protocol-v1.md`](/docs/spec/agent-adapter-protocol-v1.md).
* **Go package:** `internal/aap` — typed `HostMessage`/`AgentMessage` families,
  (de)serialization, transport env resolution, and `RunMockAdapter`.
* **Conformance kit:** `internal/aap/testdata/vectors.json` (shared wire
  vectors) and the hidden `horde aap-mock` subcommand (`cmd/aapmock.go`).
* **Not yet built:** the horde-side AAP *host* (spawning + driving real
  adapters) is a later phase; this decision lands the contract and the Go
  types.

See also the [Agent Adapter Protocol concept](/docs/knowledgebase/concepts/agent-adapter-protocol.md)
and the [agent model](/docs/knowledgebase/concepts/agent-model.md).
