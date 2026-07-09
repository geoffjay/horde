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

* `horde` — the TUI, the primary interface. Starts an in-process node.
* `horde serve` — the node server. `--daemonize` detaches it.
* `horde agent` — hidden; one ADK agent per process, invoked by the server.

# API transport

The node exposes an API for communication with clients (the TUI and other
consumers). The exact transport is intentionally a stub for a later phase
(see the [roadmap](/docs/knowledgebase/plans/roadmap.md)).

# Citations

[1] [Google ADK Go](https://github.com/google/adk-go)
[2] [Environment](environment.md)