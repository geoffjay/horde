---
type: Concept
title: Agent model
description: How ADK agents are defined, hosted, and invoked.
tags: [agents, adk, core]
timestamp: 2026-07-08T00:00:00Z
---

All agents live in the top-level `agents/` package and are built on the
Google V2 ADK (`google.golang.org/adk/v2`).

# Definition

An agent is a custom ADK agent constructed via `agent.New(agent.Config{...})`
with a `Run` function of type `func(InvocationContext) iter.Seq2[*session.Event, error]`.
See `agents/agents.go` for the `greeter` hello-world example.

# Hosting

Agents are hosted as subprocesses, not in-process. The server spawns the
horde binary itself with the hidden `agent` subcommand
(`horde agent --name <name> --socket <path>`). Each agent subprocess serves
a local HTTP API (`GET /health`, `POST /invoke` with SSE) on a unix domain
socket. The node server reads a `spawn_ready` handshake on the subprocess's
stdout to discover the socket path, then reverse-proxies invocation
requests to it. See
[subprocess agent hosting](/docs/knowledgebase/patterns/subprocess-agent-hosting.md)
and the [agent invocation transport decision](/docs/knowledgebase/decisions/agent-invocation-transport.md).

# Invocation

The agent is run through a `runner.Runner` (from `google.golang.org/adk/v2/runner`)
with an in-memory session service. The `/invoke` handler calls
`runner.Run(ctx, userID, sessionID, msg, runConfig)` and streams the
returned `iter.Seq2[*session.Event, error]` as SSE events. Each event
carries a sequential `id:` field for `Last-Event-ID` resume.

The run is decoupled from the HTTP request lifecycle: a background
goroutine appends events to a per-invocation ring buffer, and the HTTP
handler is a reader/tailer that replays from the buffer and tails new
events. Client disconnect does not cancel the run, so a reconnecting client
can resume from the buffer with `Last-Event-ID`.

# Streaming

ADK agent runs return `iter.Seq2[*session.Event, error]`; consume with
`for event, err := range … {}` rather than collecting into a slice.
