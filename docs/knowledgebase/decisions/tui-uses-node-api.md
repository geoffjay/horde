---
type: Decision
title: The TUI consumes the node API
description: The TUI always talks to a horde node over the HTTP API, even when co-located.
tags: [decision, tui, api, architecture]
timestamp: 2026-07-09T00:00:00Z
---

# Context

Phase 1 launches the TUI (`horde` with no subcommand) by starting an in-process
node in master mode and interacting with it locally. With the node API landing
in Phase 2 (see the [plan](/docs/knowledgebase/plans/phase-2-server-api.md)),
there is a choice: keep the in-process shortcut for the co-located case, or
have the TUI always go through the API.

# Decision

**The TUI always consumes the node API.** The TUI starts (or connects to) a
horde node and communicates with it exclusively over the HTTP + SSE surface
defined in the [transport decision](http-api-transport.md). There is no
in-process shortcut into `Server` methods for the co-located case.

# Consequences

* One code path for local and remote: the TUI is just another API client. This
  removes a whole class of "works in the TUI, breaks over the network" bugs
  and keeps the TUI identical whether it talks to a local master or a remote
  node.
* `Server` stays the node core; the TUI never imports `internal/server`
  directly. `internal/client` is the TUI's only adapter into the node API,
  and `internal/api` is the only adapter calling into the server.
* The TUI gains remote-node support for free once it speaks the API: pointing
  it at a remote `server.leader` is a config change, not a new code path.
* **The TUI does not start a node.** It probes
  `GET /api/v1/health` at the configured `host:port` on startup. If no node
  is reachable it shows a 60-second retry countdown (with an `[r] retry now`
  key) and never spawns a node in-process. The operator is expected to run
  `horde serve` separately; the TUI is purely a client.
* The in-process node startup that shipped in Phase 1
  (`internal/app/app.go` constructing a `*server.Server`, `WithCancel`, and
  `cmd/tui.go` calling `srv.Start`) was removed in Phase 2 and replaced with
  the client + retry UI described above.
