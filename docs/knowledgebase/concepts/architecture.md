---
type: Concept
title: Architecture
description: The horde node, master/slave modes, and agent subprocess model.
tags: [architecture, core]
timestamp: 2026-07-08T00:00:00Z
---

horde is a collection of AI agents that can be executed and managed. It runs
in two modes, presented via the [configuration](configuration.md) `mode`
field and the `horde serve --mode` flag.

# Modes

* **master** (default) — the central hub. The node is the source of truth for
  the cluster and manages local agents directly.
* **slave** — connects to a master node but is *not blocked* by that
  connection for local functionality. Local agents run immediately; the
  leader connection is established in the background.

This relationship is largely invisible to the user on each system.

# Process model

A horde node is a single long-running process (`horde serve`). It spawns and
manages agent subprocesses built on the [Google V2 ADK](https://github.com/google/adk-go).
The binary hosts its own agents as subprocesses of itself via the hidden
`horde agent --name <name>` subcommand (see
[subprocess agent hosting](/docs/knowledgebase/patterns/subprocess-agent-hosting.md)).

# Surfaces

* `horde` — the TUI, the primary interface. It is a pure client of the node
  API (`internal/client`): it probes `GET /api/v1/health` at the configured
  `host:port` and shows a 60-second retry countdown when no node is
  reachable. It never starts a node in-process (see
  [TUI consumes the node API](/docs/knowledgebase/decisions/tui-uses-node-api.md)).
* `horde serve` — the node server. `--daemonize` detaches it.
* `horde agent` — hidden; one ADK agent per process, invoked by the server.

# API transport

The node exposes an HTTP/JSON API (with SSE for streaming) for communication
with clients (the TUI and other consumers). The transport choice is recorded
in [HTTP + SSE for the node API transport](/docs/knowledgebase/decisions/http-api-transport.md)
(chi over `net/http`); the detailed surface is in the
[Phase 2 plan](/docs/knowledgebase/plans/phase-2-server-api.md). The TUI
always consumes this API (see
[TUI consumes the node API](/docs/knowledgebase/decisions/tui-uses-node-api.md)).

`internal/api` is the HTTP adapter (chi router + handlers) that calls into
`internal/server`; `internal/client` is the matching HTTP client used by the
TUI and reusable by the slave leader-client. `Server.Run` starts an
`http.Server` on `server.port` (using an injected `http.Handler` to keep the
`internal/api` → `internal/server` dependency direction clean) and serves
until ctx canceled. A fatal listener error (e.g. the port is already in use)
propagates out of `Run` rather than being logged and swallowed, so a node
never stays up with a dead API.

# Cluster & readiness

A slave registers with its master (`POST /api/v1/cluster/register`) and then
heartbeats on a ticker (`POST /api/v1/cluster/heartbeat`, carrying the slave's
node id and the names of its running agents); the master tracks registered
slaves in an in-memory registry initialized at construction, so a heartbeat
that arrives before any register — e.g. after a master restart while a slave
still believes it is connected — is handled without panicking and self-heals
on the next register. Each register/heartbeat refreshes the slave's last-seen
time; a slave not seen within three heartbeat intervals is marked stale.

The registry is observable via `GET /api/v1/cluster/nodes`, which returns the
leader id plus every registered slave (`node_id`, `addr`, `agents`,
`last_seen`, `stale`). On a slave node the registry is empty. The slave
leader-client (`internal/server/leaderclient.go`) and the master handlers
(`internal/api/cluster.go`) hand-mirror the register/heartbeat request and
response structs; an integration test
(`internal/server/integration_test.go`) drives the real leader-client against
the real `api.Router` to catch drift between the two.

Readiness reflects this: `GET /api/v1/ready` returns 200 for a master (always
ready) and for a connected slave, but **503** for a slave whose leader
connection is not established (`{status:"degraded", leader:"degraded"}`), so
orchestrators that gate on HTTP status pull a leaderless slave from rotation.
`GET /api/v1/health` remains a dumb liveness check (always 200 when the process
is up).

# Citations

[1] [Google ADK Go](https://github.com/google/adk-go)
[2] [Environment](environment.md)
