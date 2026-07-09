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
  directly. This keeps the [architecture](/docs/knowledgebase/concepts/architecture.md)
  clean — `internal/api` is the only adapter calling into the server.
* The TUI gains remote-node support for free once it speaks the API: pointing
  it at a remote `server.leader` is a config change, not a new code path.
* The TUI must start (or attach to) a node before it can do anything useful.
  For the default `horde` invocation this means spawning a local node — likely
  via the existing `daemonize` path or a lightweight in-process spawn that
  still binds the API port — rather than constructing a `Server` directly. The
  spawn mechanism is an implementation detail of the TUI startup, not a new
  surface.
* The current in-process node startup in `internal/app/app.go` is superseded
  and will be replaced during Phase 2 implementation.
