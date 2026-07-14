---
type: Decision
title: Data persistence and per-project knowledgebase
description: On-disk layout (XDG), the split between JSON KV / database / per-project config, and the per-project OKF knowledgebase as the synchronized shared brain of a project.
tags: [decision, persistence, storage, knowledgebase, projects, xdg, sync]
timestamp: 2026-07-13T00:00:00Z
---

# Context

Through Phase 3.5 Slice A, all horde state is in-memory: agent process
metadata, the execution context store, and the (empty) project/issue fields.
A node restart loses everything. That was correct for Slice A — execution
context is observability state materialized from live agent signals and
rebuilds on reconnect. Slice B (projects, teams, multi-turn context) is the
first state that represents **user work product**: a project's goal, its team
composition, agent assignments, and accumulated conversation history. Losing
that on restart destroys real work. Persistence becomes a priority at Slice B,
not Phase 4.

This decision settles the on-disk layout, the storage strategy (not everything
in a relational database by design), the global/per-project config split, and
the per-project knowledgebase — horde's central differentiator.

# Scope

* Target platforms: **Linux and macOS**. Single-user: one user running horde.
  System-wide multi-user installations are explicitly out of scope.
* Local persistence (survive node restart) is settled here.
  Cross-node durability and replication are Phase 4 and build on top of this.
* The knowledgebase sync protocol is outlined here as a direction; its full
  specification is a separate document (Phase 3.6 / 4).

# 1. On-disk layout (XDG)

horde follows the XDG Base Directory specification, scoped to a single user.

| Path | Purpose | Contents |
| --- | --- | --- |
| `~/.config/horde/` | Configuration | `horde.yaml` (or json/toml), global project defaults |
| `~/.local/share/horde/` | General storage | Logs, auth tokens, account info, session data, **database files** |
| `~/.local/state/horde/` | Ephemeral / trivial state | JSON KV stores, execution state, agent info, prompt history, lock files |

### Rationale for the split

* **config** is declarative, user-authored, and portable. It already exists
  in the config search path (`docs/environment.md`). Global project defaults
  live here too.
* **share** persists across sessions and is valuable enough to back up.
  Database files (requiring migrations) go here, alongside logs and auth.
* **state** is trivially reconstructible or ephemeral: the kind of thing you
  *could* delete without losing work, but keeping it improves startup time or
  UX (e.g. prompt history, last-known agent state, lock files). JSON KV is
  sufficient — no schema needed.

### Env overrides

Following the XDG convention, these paths are overridable:

| Env var | Default | Description |
| --- | --- | --- |
| `HORDE_PATHS_CONFIG_DIR` | `~/.config/horde` | Configuration directory. |
| `HORDE_PATHS_DATA_DIR` | `~/.local/share/horde` | General storage (logs, auth, DBs). |
| `HORDE_PATHS_STATE_DIR` | `~/.local/state/horde` | Trivial state (KV, locks, history). |

These complement the existing `HORDE_CONFIG` (explicit config file path) and
the `HORDE_*` env-prefix convention. The config search path in
`docs/environment.md` is updated to reflect `~/.config/horde/` as the XDG
home rather than a fallback.

# 2. Storage strategy — not everything in a database

The project deliberately avoids putting all state in a relational database.
Each data category is evaluated for whether it benefits from a schema +
migrations or is better served by schema-less JSON. The default is JSON;
the database is opt-in where query patterns or relational integrity demand it.

## Categories

### JSON KV (state dir)

Schema-less, trivially reconstructible, read whole-file or by key:

* **Execution state** — agent activity, blocked/waiting flags, current turn.
  This is the materialized execution context (Slice A) flushed to disk so a
  restart can restore the last-known view before agents reconnect.
* **Agent info** — running agents, their socket paths, health status. Rebuilt
  on startup but persisted for fast restore.
* **Prompt history** — the user's recent prompts (TUI command history), bounded
  and ephemeral.
* **Lock files** — node singleton lock, project locks.

### Per-project JSON (in the project workspace)

Lives in `.horde/` within the project's workspace directory (see §3). This is
data that belongs *to the project* and should travel with it:

* **Project settings** — per-project overrides of global config.
* **Project metadata** — name, goal, state, created/updated timestamps.
* **Agent-project assignments** — which agents are on the team. This is per-
  project by nature (an agent's active project is one at a time), so it lives
  with the project, not in a global store.

### Database with migrations (data dir)

For state that is relational, cross-project, or benefits from indexed
queries and schema evolution:

* **Teams** — a team can span multiple projects (the same agents and, in 3.5b,
  users appear across projects). A team is a cross-project entity, which
  makes it a natural database citizen. A `teams` table with a join to
  `projects` and `agents` avoids denormalizing team membership across
  per-project JSON files.
* **Session/conversation history** — multi-turn context (`session_id` →
  message history) grows unboundedly and is read by turn ranges. An embedded
  database with indexed session keys is the right fit. This is the largest
  and most query-sensitive data category.
* **Audit log** — if introduced, append-only and indexed by time/agent/project.

### Candidates evaluated and deferred

* **Projects** — *could* be a database table, but a project's core metadata
  (name, workspace, goal, state) is simple enough for a JSON list or per-
  project file. The project store starts as **JSON in the state dir** (a
  `projects.json` index) with per-project detail in the workspace's `.horde/`
  directory. If the query patterns or the cross-node aggregation in Phase 4
  demand it, projects migrate to the database. The store interface (see §5)
  makes this a swappable detail, not an architectural commitment.
* **Auth / account** — arrives in 3.5b. Will likely use the database. Not
  decided here.

## Database choice

An **embedded** database — no external server, single file in the data dir.
SQLite (via `modernc.org/sqlite` for pure-Go, or `mattn/go-sqlite3` for
cgo) is the obvious candidate. Migrations via `golang-migrate/migrate` or
`pressly/goose`. The specific library is a separate, smaller decision
deferred to implementation; the constraint is: embedded, single-file,
migratable, Linux + macOS.

# 3. Global vs per-project configuration

horde has two configuration layers for project behaviour:

* **Global** (`~/.config/horde/`) — the user's defaults for all projects:
  default workspace dir, default agents, retention policies, logging. This is
  the existing `horde.yaml` / `HORDE_*` config system.
* **Per-project** (`.horde/` in the project workspace) — overrides that apply
  only when horde is working in that project. **Per-project takes precedence
  over global.** This mirrors how git's `.gitconfig` per-repo overrides
  `~/.gitconfig`.

### `.horde/` directory layout

```
<workspace>/
  .horde/
    config.yaml          # per-project config overrides (global ← these)
    project.json         # project metadata: name, goal, state, timestamps
    team.json            # team composition (agents; users in 3.5b)
    knowledgebase/       # the OKF knowledge base (see §4)
      index.md
      concepts/
      decisions/
      patterns/
      plans/
      log.md
```

Per-project config is loaded *after* global config in the layered loader, so
it overrides. The existing `internal/config` loader is extended with a
project-local search path when a project workspace is active.

# 4. Per-project knowledgebase

Every project has a knowledgebase. It is not optional.

## What it is

A structured knowledge base conforming to the [Open Knowledge Format (OKF)
v0.1](https://github.com/GoogleCloudPlatform/knowledge-catalog/blob/main/okf/SPEC.md)
— the same format horde's own `docs/knowledgebase/` uses. It is the project's
**shared brain**: the accumulated context that lets distributed participants
(people and AI agents) work on the project with shared understanding.

## Why it is per-project

The knowledgebase describes the project it belongs to — its architecture
decisions, patterns, plans, environment. It is created with the project and
lives in `.horde/knowledgebase/` within the workspace. It travels with the
project (checked into git, if the workspace is a repo) and is the primary
artifact that agents consult before acting.

## Sync — the distributed knowledgebase

A central goal of horde: distributed users participate in a project with a
shared knowledgebase, and AI agents execute against it. When a node is
registered to a project, its local `.horde/knowledgebase/` is **synchronized**
with the other nodes on that project.

This is the hardest problem in this decision and is not fully specified here.
The outline:

* **While connected and registered** to a project, a node's knowledgebase
  changes propagate to the other project nodes, and remote changes are
  applied locally.
* **Nodes joining** must reconcile against the current state — either a full
  sync from the master (or a designated knowledgebase authority) or a
  merge.
* **Nodes leaving** must not corrupt the shared state; their unpushed changes
  are preserved locally and reconciled on rejoin.
* **Conflict resolution** — OKF docs are files; the sync model is file-based
  (not record-based). A CRDT-like approach or a last-writer-wins-with-timestamps
  model, possibly with a merge protocol for structured frontmatter. The OKF
  spec's `timestamp` field in frontmatter is designed for this.
* **Protocol** — this requires its own protocol for safely handling join/leave
  and change resolution. It is a distinct concern from the agent execution
  context or the project/team model. It is scoped to Phase 3.6 (AAP host —
  agents need the KB) and Phase 4 (distributed — multi-node KB sync), with a
  dedicated spec doc.

### What this means for Slice B

Slice B creates the knowledgebase directory with a new project (seeds
`index.md`, `log.md`, and the category directories). It does **not** implement
sync — that is a later phase. But the store interface and the project model
must account for the knowledgebase from the start, so sync can be added
without reshaping the project model.

# 5. Store interfaces

To avoid hardcoding in-memory assumptions that persistence would undo, the
Slice B store types expose interfaces from the start:

```go
// ProjectStore persists project metadata and team composition.
// v1: in-memory + JSON flush to state dir; v2: optional database backend.
type ProjectStore interface {
    Create(p Project) error
    Get(id string) (*Project, error)
    List() ([]Project, error)
    Update(p Project) error
    Delete(id string) error
    AssignAgent(projectID, agentID string) error
    RemoveAgent(projectID, agentID string) error
}

// SessionStore persists multi-turn conversation history.
// v1: delegates to ADK InMemoryService; v2: database-backed.
type SessionStore interface {
    GetOrCreate(sessionID string) (Session, error)
    Append(sessionID string, msg Message) error
    History(sessionID string) ([]Message, error)
}
```

The `Server` holds a `ProjectStore` and `SessionStore` (interfaces), not
concrete in-memory types. Slice B ships in-memory implementations; the
persistence layer swaps in behind the same interfaces.

# Consequences

* On-disk layout follows XDG (`~/.config/horde`, `~/.local/share/horde`,
  `~/.local/state/horde`), overridable via `HORDE_*_DIR` env vars.
* Not everything is in a database: trivial state is JSON KV, project metadata
  is per-project JSON in `.horde/`, and relational/cross-project data (teams,
  session history) uses an embedded database with migrations.
* Per-project config in `.horde/config.yaml` overrides global config.
* Every project has an OKF knowledgebase in `.horde/knowledgebase/`, created
  at project creation, traveling with the workspace.
* Knowledgebase sync across distributed nodes is a future phase with its own
  protocol; Slice B creates the KB but does not sync it.
* Store interfaces (`ProjectStore`, `SessionStore`) are introduced in Slice B
  so the persistence backend is swappable without reshaping the server.
* `docs/environment.md` is updated with the new data/state/config paths and
  env overrides.
* The config search path gains `~/.config/horde/` as the canonical XDG config
  home (already listed as a fallback, now primary).

# Related

* [Project, team, and user model](project-team-user-model.md) — the data model
  this decision persists.
* [Agent execution context](/docs/knowledgebase/plans/agent-execution-context.md)
  — Slice A state that is flushed to the state dir as JSON KV.
* [Roadmap](/docs/knowledgebase/plans/roadmap.md) — Phase 3.6 (AAP host,
  agents need the KB) and Phase 4 (distributed sync).
