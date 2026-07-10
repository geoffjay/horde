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

# Phase 3 — Agent mechanism (current)

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

# Phase 3.5 — Multi-agent context (planned)

* Users and authentication on the node API.
* Projects as a unit of work (workspace, goal/state, assigned agents).
* Teams of agents within a project (roles, capabilities).
* Permissions: filesystem access scope, tool allowlist, agent-to-agent
  messaging boundaries.
* Multi-turn context across invocations (conversation state per project
  session).
* The invocation payload grows from `{message}` to include project/team/
  session context.

This phase requires a decision doc on "what is a project / what is a team"
before a plan doc, because the answers constrain the agent invocation
contract.

# Phase 4 — Distributed

* Slave registration with the master.
* Agent placement and coordination across nodes.
* Cluster discovery beyond `static` (dns, gossip).
* Cross-node event fan-out (the event bus may gain an nng or HTTP
  fan-out layer here).
