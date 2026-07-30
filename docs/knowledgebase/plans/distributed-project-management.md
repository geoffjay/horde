---
type: Plan
title: Distributed project management
description: Forward project mutations from worker nodes to the coordinator, and add a horde project CLI subcommand so projects can be created from any node without curl.
tags: [plan, distributed, projects, cli, phase-4]
timestamp: 2026-07-14T00:00:00Z
---

# Problem

Project state (`projects.json`) is local to each node. A worker that receives
`POST /api/v1/projects/` creates the project in its own store; the coordinator never
sees it. The user expects to manage projects from whichever node is local to
them, but the system has no forwarding path. Separately, the only way to create
a project today is `curl` — there is no `horde project` CLI subcommand.

This was surfaced during TUI development: a user running a worker node
connected to a remote coordinator tried to create a project via the local node API
and it either silently created a local-only project or failed depending on
configuration. See the [coordinator/worker model
decision](/docs/knowledgebase/decisions/coordinator-worker-model.md) and the [TUI
plan's out-of-scope note](tui-projects.md) (cross-node project listing is
Phase 4 sync).

# Scope

Two independent but related pieces:

1. **Project forwarding** — worker nodes forward project mutations to the
   coordinator so projects are coordinator-authored and visible cluster-wide.
2. **`horde project` CLI** — a cobra subcommand for creating, listing, and
   managing projects without `curl`.

Not blocking the TUI slices (5+): they work against any node that has projects.
Creating the project on the coordinator directly is a sufficient workaround until
this lands.

# 1 — Project forwarding

## Current state

* `ProjectStore` (`internal/server/project.go`) is an in-memory or persistent
  JSON store, local to each node.
* `Server.CreateProject` (`internal/server/project_api.go`) writes to the local
  store, spawns agents locally, and scaffolds the knowledgebase.
* The API handlers (`internal/api/projects.go`) call `projectView` methods
  directly — there is no mode check or coordinator forwarding.
* Workers already have a `leaderClient` (`internal/server/leaderclient.go`) used
  for `POST /cluster/register` and `POST /cluster/heartbeat`; it can be reused.

## Proposal

Worker nodes forward project mutations to the coordinator and return the coordinator's
response to the caller. The worker does not write to its own project store for
forwarded mutations. Reads (`GET /projects`, `GET /projects/{id}`) also proxy
to the coordinator so the worker sees the cluster-wide project list.

### Forwarded endpoints

| Method | Path | Forwarded? |
|--------|------|-----------|
| GET | `/projects/` | yes (read from coordinator) |
| POST | `/projects/` | yes (create on coordinator) |
| GET | `/projects/{id}` | yes (read from coordinator) |
| POST | `/projects/{id}/pause` | yes |
| POST | `/projects/{id}/resume` | yes |
| POST | `/projects/{id}/finish` | yes |
| POST | `/projects/{id}/agents` | yes |
| DELETE | `/projects/{id}/agents/{agentID}` | yes |

### Implementation sketch

* Add a `projectForwarder` that wraps the leader client for project mutations.
  On a worker, the API handlers check `srv.Mode()`; if worker, they forward to the
  coordinator via the leader client and return the coordinator's response. On a coordinator,
  they use the local store as today.
* Agent spawning stays local: when the coordinator creates a project with agents,
  it spawns agents on the coordinator. A follow-up (Phase 4 agent placement) can
  spread agents across nodes; for now the coordinator hosts them.
* The worker's `GET /projects` proxies to the coordinator so the TUI (connected to
  the worker) sees the full project list.
* Error handling: if the worker can't reach the coordinator, project mutations
  return `502 Bad Gateway` with a clear error message (not a silent local
  write).
* The `leaderClient` already manages the coordinator address and retry; reuse it
  rather than adding a separate HTTP client.

### What does not change

* The coordinator's project store remains the source of truth.
* The worker's local `projects.json` is not used for projects in worker mode
  (it may still exist for standalone-worker mode where no leader is configured).
* Agent execution contexts remain node-local; the cluster context aggregation
  (Slice A) already handles cross-node visibility.

### Standalone worker edge case

A worker with no configured leader (`leader = ""`) runs standalone — it has no
coordinator to forward to. In this case project mutations fall back to the local
store (current behaviour). This is the "worker without a coordinator" mode the
[coordinator/worker decision](/docs/knowledgebase/decisions/coordinator-worker-model.md)
already allows.

## 2 — `horde project` CLI

A new cobra subcommand (`cmd/project.go`) following the
[one-file-per-command pattern](/docs/knowledgebase/patterns/one-file-per-command.md).

### Subcommands

```
horde project create --name <name> --workspace <path> --goal <text> --agents <name1,name2>
horde project list [--state active|paused|finished]
horde project show <id>
horde project pause <id>
horde project resume <id>
horde project finish <id>
horde project assign <project-id> --agent <name>
horde project remove <project-id> <agent-id>
```

### Target node

* By default talks to `localhost:13420` (the local node). A `--node` flag
  overrides the target address. This mirrors how the TUI resolves its target
  (`cmd/tui.go` reads `HORDE_SERVER_PORT` / config).
* On a worker with forwarding (part 1), `horde project create` hitting the
  local worker transparently creates the project on the coordinator. Without
  forwarding, the user points `--node` at the coordinator.
* The subcommand uses `internal/client` (the same client the TUI uses) — no
  duplicated HTTP plumbing.

### Output

* `create`: print the project id and name (JSON or table; match `horde` CLI
  style).
* `list`: a table with id, name, state, agent count — similar to the TUI
  projects home but in plain text.
* `show`: full project detail (state, workspace, goal, team).
* Lifecycle commands: print the updated state.

# Implementation order

1. **CLI subcommand** (no forwarding dependency) — `horde project create/list/
   show/pause/resume/finish/assign/remove` against `internal/client`. Works
   against any node; user points at the coordinator manually.
2. **Project forwarding** — worker API handlers proxy to coordinator. The CLI then
   works against any node transparently.

Step 1 is independently useful and unblocks TUI testing without `curl`. Step 2
is the distributed-systems work that makes the local-node workflow correct.

# Out of scope

* **Cross-node agent placement** — when the coordinator creates a project with
  agents, they spawn on the coordinator. Distributing agents across worker nodes is
  Phase 4 (agent placement and coordination).
* **Project sync to workers** — the forwarding model is request-proxy, not
  active replication. Workers don't cache the full project list; they proxy
  reads. Active replication (workers holding a copy) can come with Phase 4 sync
  if the proxy round-trip is too slow.
* **Per-user auth** — who can create/pause/finish projects is 3.5b. Until then
  any API caller can mutate.
