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
(`horde agent --name <name>`). See
[subprocess agent hosting](/docs/knowledgebase/patterns/subprocess-agent-hosting.md).

# Invocation

For this first version the agent host blocks until asked to stop; the agent
is constructed to validate wiring. Real invocation driven by the server API
is a later phase (see the [roadmap](/docs/knowledgebase/plans/roadmap.md)).

# Streaming

ADK agent runs return `iter.Seq2[*session.Event, error]`; consume with
`for event, err := range … {}` rather than collecting into a slice.