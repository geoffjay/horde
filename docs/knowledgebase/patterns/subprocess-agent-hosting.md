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
horde agent --name <name>
```

`server.SpawnAgent` uses `os.Executable()` to find the binary, then
`exec.CommandContext` to start one process per agent. Each process is
tracked (`agentProc`) and torn down (SIGTERM, then SIGKILL after a grace
period) when the server stops.

# Rationale

Isolates agents as separate processes for fault isolation and future
resource controls, while keeping a single deployable binary. See
[architecture](/docs/knowledgebase/concepts/architecture.md) and the
[agent model](/docs/knowledgebase/concepts/agent-model.md).