---
type: Plan
title: Roadmap
description: Phasing of horde capabilities.
tags: [plan, roadmap]
timestamp: 2026-07-08T00:00:00Z
---

# Phase 1 — Foundation (current)

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

# Phase 2 — Server API

* Implement the node API transport (the stub currently in `Server.Run`).
* Define the API surface for TUI ↔ server and slave ↔ master.
* Real leader connection / health / registration.

# Phase 3 — Agents

* Replace the hello-world greeter with real agents.
* Drive agent invocation from the server API.
* Agent lifecycle: spawn, list, stop, stream events.

# Phase 4 — Distributed

* Slave registration with the master.
* Agent placement and coordination across nodes.
* Cluster discovery beyond `static` (dns, gossip).