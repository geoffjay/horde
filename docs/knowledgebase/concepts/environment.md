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
* `horde` — the TUI; a pure client of the node API (does not start a node).

# Auth (opt-in)

Per-user API-token auth is configured via the `auth.users` block and presented
by clients via `HORDE_USER_TOKEN` / `--token`. Empty disables auth. See
[`docs/environment.md`](../../../environment.md) and the
[Phase 3.5b plan](../plans/phase-3.5b-auth.md).

# Knowledgebase sync (opt-in)

Per-project OKF knowledgebase synchronization (KSP v1) is configured via the
`knowledgebase.sync` block: `enabled`, `watch_local` (stage 2 — participant
watches its local tree and pushes edits), `workspace_root`, `poll_interval`,
`debounce`, `max_file_size`, and `ignore`. Empty/disabled ⇒ byte-for-byte
current (local, git-backed) behavior. See
[`docs/environment.md`](../../../environment.md), the
[knowledgebase sync plan](../plans/knowledgebase-sync.md), and
[KSP v1](/docs/spec/knowledgebase-sync-protocol-v1.md).

# Integration environment

`docker/docker-compose.yml` defines one master and two slaves from a single
image. Host ports 13420 (master), 13421 (slave1), 13422 (slave2).
