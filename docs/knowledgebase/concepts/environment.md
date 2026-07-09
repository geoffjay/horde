---
type: Concept
title: Environment
description: Ports, services, and environment variables for horde.
resource: /docs/environment.md
tags: [config, environment, core]
timestamp: 2026-07-08T00:00:00Z
---

All environment data — ports, configuration keys, environment variables, and
services — is documented in detail in [`docs/environment.md`](../../../environment.md).
This concept exists so the knowledge base references it as a first-class
idea.

# Ports

| Port  | Service        | Notes                                   |
|-------|----------------|-----------------------------------------|
| 13420 | horde node API | Default node API port (`server.port`).  |
| 13500 | horde test API| Used in test fixtures.                   |

# Services

* `horde serve` — the node (master or slave).
* `horde agent` — hidden; hosts one ADK agent per subprocess.
* `horde` — the TUI; starts an in-process node.

# Integration environment

`docker/docker-compose.yml` defines one master and two slaves from a single
image. Host ports 13420 (master), 13421 (slave1), 13422 (slave2).