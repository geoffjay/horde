---
type: Pattern
title: Subprocess agent hosting
description: The horde binary hosts its own ADK agents as subprocesses.
tags: [pattern, agents, processes]
timestamp: 2026-07-08T00:00:00Z
---

# Pattern

The horde binary serves as its own agent host. The server spawns a
subprocess of itself for each agent using the hidden `agent` subcommand:

```
horde agent --name <name> --socket /tmp/horde-agent-{id}.sock
```

Each agent subprocess starts an HTTP server on a unix domain socket and
serves `GET /health` and `POST /invoke` (SSE). The subprocess emits a
`spawn_ready` NDJSON line on stdout at startup to announce the socket path;
the server reads this during `SpawnAgent` and records it on the `agentProc`.

`server.SpawnAgent` uses `os.Executable()` to find the binary, then
`exec.CommandContext` to start one process per agent. The agent name is
looked up in the `agents/` registry (`agents.Get(name)`) before spawning —
unknown agents fail before any subprocess is started. Each process is
tracked (`agentProc` with `socketPath` and `healthy` fields) and torn down
(SIGTERM, then SIGKILL after a grace period) when the server stops. The
socket file is removed on process exit.

The node's `POST /api/v1/agents/{id}/invoke` handler reverse-proxies the
SSE stream from the agent's unix socket to the API client, rewriting the
path to `/invoke`. See the [transport decision](/docs/knowledgebase/decisions/agent-invocation-transport.md).

# Rationale

Isolates agents as separate processes for fault isolation and future
resource controls, while keeping a single deployable binary. See
[architecture](/docs/knowledgebase/concepts/architecture.md) and the
[agent model](/docs/knowledgebase/concepts/agent-model.md).
