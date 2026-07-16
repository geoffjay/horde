---
type: Plan
title: Roadmap
description: Phasing of horde capabilities.
tags: [plan, roadmap]
timestamp: 2026-07-08T00:00:00Z
---

# Phase 1 — Foundation (complete)

* CLI with cobra, one file per command.
* Master/slave node modes; `horde serve --mode`.
* Layered configuration system (vendored plantd config).
* logrus logging.
* Hello-world ADK agent (`greeter`) in `agents/`.
* Subprocess agent hosting via `horde agent`.
* TUI (bubbletea + lipgloss).
* Docker integration environment (master + 2 slaves).
* Taskfile, GitHub Actions (lint, build, test).
* OKF knowledge base.

# Phase 2 — Server API (complete)

Detailed plan: [Phase 2 — Server API](phase-2-server-api.md).

* Implement the node API transport (the stub previously in `Server.Run`).
* Define the API surface for TUI ↔ server and slave ↔ master.
* Real leader connection / health / registration.

Decisions underpinning this phase:

* [HTTP + SSE transport](/docs/knowledgebase/decisions/http-api-transport.md)
* [TUI consumes the node API](/docs/knowledgebase/decisions/tui-uses-node-api.md)

# Phase 3 — Agent mechanism (complete)

Detailed plan: [Phase 3 — Agent mechanism](phase-3-agents.md).

* Agent subprocess serves a local HTTP API on a unix domain socket.
* Node server reverse-proxies `POST /api/v1/agents/{id}/invoke` to the agent.
* Long-lived agents with concurrent invocations.
* `Last-Event-ID` resume for interrupted SSE streams.
* Real agent registry (`agents.Get(name)`); `--name` selects the agent.
* Structurally real non-LLM agents (streaming, multi-turn within one
  invocation). LLM-backed agents deferred.
* Hung-agent detection via periodic `GET /health` polling.

Decisions underpinning this phase:

* [HTTP over unix domain sockets for agent invocation](/docs/knowledgebase/decisions/agent-invocation-transport.md)

# Phase 3.5 — Multi-agent context (complete)

Decision doc: [Project, team, and user model](/docs/knowledgebase/decisions/project-team-user-model.md).

Built in two slices:

## Slice A — Agent execution context (complete)

Detailed plan: [Agent execution context](agent-execution-context.md).

* `ExecutionContext` data model, node-side materialization from AAP frames +
  launch metadata.
* Local query API (snapshot + change stream).
* Cross-node aggregation via the master with read-only, redacted remote
  access.
* Minimal node-granular principal model (`local` vs `remote`).

Signal fidelity note: full AAP `context`/`error`/`approval` frames arrive with
the AAP host (Phase 3.6, complete); native ADK agents yield only coarse context
(activity + errors). Slice A ships the model, API, and aggregation regardless.

## Slice B — Projects, teams, and multi-turn context (complete)

Detailed plan: [Projects, teams, and multi-turn context](projects-teams.md).

* Projects as a unit of work: workspace path, free-text goal, lifecycle
  states (active/paused/finished).
* Teams of users and agents; agents are peers with no roles; one agent
  active in one project at a time.
* Agent-to-project assignment; session key = `(agent_id, project_id)` for
  private multi-turn context per agent.
* Advisory filesystem scope (no OS-level sandboxing).
* No per-user auth, no tool allowlist, no agent-to-agent messaging.

## Deferred to 3.5b

* Per-user authentication on the node API.
* Per-user project ownership and permission scopes.
* Per-user tool restrictions.
* OS-level filesystem sandboxing.

This split lets us build the project/team model and execution context
without committing to an auth mechanism. When 3.5b lands, the project/team
model already has the right shape — it just gains an `owner` field and
access control.

# Phase 3.6 — AAP host (external coding agents) (complete)

Detailed plan: [AAP host — driving external coding agents](aap-host.md).

Decision: [Adopt the Agent Adapter Protocol (AAP)](/docs/knowledgebase/decisions/agent-adapter-protocol.md).
Spec: [Agent Adapter Protocol v1](/docs/spec/agent-adapter-protocol-v1.md).

The product path: drive **external** AI coding agents (Claude Code and others)
through AAP adapters — "coding, but for documents" over the OKF knowledge base.
Phases 3–3.5 build the mechanism and the project/team scaffolding on native ADK
agents; this phase is where real coding agents plug in.

* Node spawns AAP adapters over the stdio binding (NDJSON): the
  `initialize`→`ready` handshake, the prompt/turn loop, and graceful shutdown.
* A second agent *kind* alongside native ADK: AAP agents are declared in
  config (`agents.<name>.kind: aap`) rather than registry-built; both kinds
  share the `agentProc` map, the invoke API, and project assignment.
* Tool approval wired to node policy (the node is the sole approval
  authority); the project workspace mapped onto AAP `workspace.cwd` +
  `initialize.permissions`.
* Consume AAP `context`/`error`/`approval_request` frames to populate the
  [agent execution context](agent-execution-context.md) at full fidelity —
  this lights up the `applyStatus`/`applyContextUpdate`/`applyError`/
  `applyApprovalRequest` receivers Slice A left waiting.
* The `horde aap-mock` fixture driven end to end as the first adapter. The
  first real adapter — **pi-aap** (for the `pi` coding agent) — is now wired
  and handshake-verified through the host (`TestSpawnAAPAgent_PiAdapter`,
  opt-in via `HORDE_TEST_PI_ADAPTER`); a live turn against a model is verified
  manually. See [`docs/examples/pi-agent.yaml`](/docs/examples/pi-agent.yaml).

Independent of per-user auth (3.5b): can land before or after it. Foundation
already in place — the AAP spec and the `internal/aap` package (typed messages,
  mock adapter, shared test vectors).

# Phase 4 — Distributed

* Slave registration with the master.
* Agent placement and coordination across nodes.
* Cluster discovery beyond `static` (dns, gossip).
* Cross-node event fan-out (the event bus may gain an nng or HTTP
  fan-out layer here).
