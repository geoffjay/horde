---
type: Plan
title: Projects, teams, and multi-turn context
description: Phase 3.5 Slice B — the project/team data model, the project API, agent-to-project assignment, and session-key derivation for private multi-turn context per (agent_id, project_id).
tags: [plan, agents, projects, teams, multi-turn, phase-3.5]
timestamp: 2026-07-13T00:00:00Z
---

This plan lands the project/team model on top of the [Phase 3 agent
mechanism](phase-3-agents.md) and the [Slice A agent execution
context](agent-execution-context.md). The what/why is settled in the
[decision](/docs/knowledgebase/decisions/project-team-user-model.md); this
document is the how.

* Decision: [Project, team, and user model](/docs/knowledgebase/decisions/project-team-user-model.md).
* Decision: [Data persistence and per-project knowledgebase](/docs/knowledgebase/decisions/persistence-and-knowledgebase.md).
* Mechanism: [Phase 3 — Agent mechanism](phase-3-agents.md).
* Observability already in place: [Agent execution context](agent-execution-context.md) (Slice A) — its `Project`/`Issue` fields are seeded by this slice.

# Scope

**v1 delivers:** the `Project` and `Team` data models; a node-side project store;
the project API (create/list/get/pause/finish); agent-to-project assignment and
reassignment; advisory filesystem scope (workspace path passed to the agent at
spawn); and session-key derivation `(agent_id, project_id)` wired through the
existing invoke path so an agent retains a private conversation history per
project.

**v1 does not deliver:** per-user authentication or ownership (3.5b), per-user
permission scopes or tool allowlists (3.5b), OS-level filesystem sandboxing
(advisory only), agent-to-agent messaging (deferred), cross-node project
sync (Phase 4), knowledgebase synchronization across nodes (Phase 3.6/4,
see the [persistence decision](/docs/knowledgebase/decisions/persistence-and-knowledgebase.md)),
or a database-backed store (Slice B ships in-memory + JSON flush; the
database backend swaps in behind the same store interfaces).

**Depends on:** Phase 3 (agent spawn/invoke mechanism) and Slice A (execution
context store, which gains seeded `Project`/`Issue`). Both complete.

**Persistence:** Slice B is the first state that represents user work
product. The [persistence decision](/docs/knowledgebase/decisions/persistence-and-knowledgebase.md)
settles the on-disk layout (XDG), the storage strategy (JSON KV / database /
per-project), and the per-project OKF knowledgebase. Slice B introduces
store interfaces (`ProjectStore`, `SessionStore`) with in-memory + JSON
flush implementations; the database backend swaps in later without
reshaping the server. Every project gets a knowledgebase directory created
at project creation (`.horde/knowledgebase/`); sync is deferred.

# Data model

```go
// Project is the unit of work.
type Project struct {
    ID        string    `json:"id"`
    Name      string    `json:"name"`
    Workspace string    `json:"workspace"` // filesystem dir; advisory scope
    Goal      string    `json:"goal"`       // free-text
    State     ProjectState `json:"state"`   // active | paused | finished
    Team      Team      `json:"team"`
    CreatedAt time.Time `json:"created_at"`
    UpdatedAt time.Time `json:"updated_at"`
}

type ProjectState string

const (
    ProjectActive   ProjectState = "active"
    ProjectPaused   ProjectState = "paused"
    ProjectFinished ProjectState = "finished"
)

// Team is the set of users and agents assigned to a project.
type Team struct {
    Agents []TeamAgent `json:"agents"`
    // Users are absent in 3.5a (node-owned projects, no per-user auth).
    // The field is reserved so 3.5b can add it without reshaping the API.
    Users []TeamUser `json:"users,omitempty"`
}

type TeamAgent struct {
    AgentID    string    `json:"agent_id"`
    Name       string    `json:"name"`        // the agent kind (greeter, ...)
    AssignedAt time.Time `json:"assigned_at"`
}

type TeamUser struct {
    UserID string `json:"user_id"`
    // added in 3.5b
}
```

**Design notes**

* A project is owned by the node in 3.5a — there is no `owner` field yet. 3.5b
  adds `owner` and access control; the struct already has the right shape.
* A team is never empty: a project is created with at least one agent (or, in
  3.5b, one user). Creation with no members is rejected.
* One agent active in one project at a time: assigning an agent that is already
  active elsewhere reassigns it (its session key changes, its prior
  conversation history is retained by the prior project's session and is not
  carried over).

# Layer 1 — Project store

The node keeps a `ProjectStore` (interface, per the [persistence
decision](/docs/knowledgebase/decisions/persistence-and-knowledgebase.md))
guarded by the server mutex, alongside `ctxStore`. The v1 implementation is
in-memory with JSON flush to `~/.local/state/horde/` (a `projects.json`
index) and per-project detail in the workspace's `.horde/` directory. It is
updated from:

1. **Create** — `CreateProject` seeds a new `Project` with `State=active` and
   the supplied team of agents (spawning any not already running).
2. **State transition** — `PauseProject` / `FinishProject` flip `State` and
   gate further invokes (paused projects reject invokes; finished projects
   release their agents' active-project binding and retain context for
   `agent.context_retention` before eviction, reusing the Slice A retention
   path).
3. **Assignment** — `AssignAgent(projectID, agentID)` rebinds an agent's
   active project, updates the team, and seeds the execution context's
   `Project`/`Issue` fields via the existing `ctxStore` (the setter Slice A
   deferred).

## Agent ↔ project binding

`agentProc` (`internal/server/server.go:137`) gains an `activeProject string`
field. `SpawnAgent` and `AssignAgent` set it; `FinishProject` clears it. The
invoke path reads it to derive the session key. An agent with no active
project has an empty `activeProject` and is still invokable — the node omits
`session_id` and the agent falls back to Phase 3 per-invocation semantics
(see Layer 3).

# Layer 2 — Project API

```
POST   /api/v1/projects                  → create project (body: name, workspace, goal, agent names)
GET    /api/v1/projects                  → list projects (optional ?state= filter)
GET    /api/v1/projects/{id}             → project snapshot
POST   /api/v1/projects/{id}/pause       → pause
POST   /api/v1/projects/{id}/resume      → resume (active ← paused)
POST   /api/v1/projects/{id}/finish      → finish
POST   /api/v1/projects/{id}/agents      → assign agent to project (body: name)
DELETE /api/v1/projects/{id}/agents/{agentID} → remove agent from project
```

A new `projectView` interface is added to `internal/api/types.go` (alongside
`agentView`/`clusterView`) and asserted against `*server.Server`. Handlers
follow the existing `func(srv projectView) http.HandlerFunc` pattern; routes
register under `r.Route("/projects", ...)` in `internal/api/router.go`.

The project API is **local only** in 3.5a — no cluster-wide project surface
(that arrives with Phase 4 cross-node sync). A `remote` principal cannot list
or mutate another node's projects; the `local`/`remote` seam from Slice A is
reused.

# Layer 3 — Multi-turn context (session-key derivation)

**Today** (`internal/api/invoke.go`, `internal/agentapi/handler.go`): the node
reverse-proxies `POST /api/v1/agents/{id}/invoke` to the agent subprocess
without reading the body; the agent subprocess derives `sessionID = invID`
(the invocation id) for `runner.Run`.

**After Slice B:** the node derives `session_id` from `(agent_id, project_id)`
— specifically `"<agent_id>:<project_id>"` — and injects it into the invoke
request body before proxying. The agent subprocess's `invokeRequest`
(`internal/agentapi/handler.go:76`) gains a `SessionID` field and uses it (not
`invID`) as the `sessionID` arg to `runner.Run`. `invID` continues to drive
the Phase 3 resume broker; the two are distinct and coexist, exactly as the
decision specifies.

This is the **only** invoke-payload change. The `POST /agents/{id}/invoke`
URL and response shape are unchanged; `session_id` is an added body field.

### Body injection in the reverse proxy

`internal/api/invoke.go` is a streaming reverse proxy today. To inject
`session_id` without buffering arbitrarily large bodies (the invoke message
is small JSON), the proxy gains a minimal body-rewrite: read the request body
(sized, JSON), set `session_id`, re-marshal, and forward. This keeps the
proxy pattern intact while making the project implicit at the wire level. A
`local`/`remote` check is not needed here (project API is local-only; the
invoke path is local to the owning node).

### No active project — fallback, not rejection

Per the [decision doc](/docs/knowledgebase/decisions/project-team-user-model.md),
an agent with no active project is **still invokable**. Projects are the unit
of work and multi-turn context, not a precondition for an agent to respond.
When the agent has no active project:

* the node omits `session_id` (or sends it empty) in the proxied body;
* the agent subprocess falls back to the Phase 3 behaviour — it uses
  `invocation_id` as the `sessionID` for `runner.Run`, yielding a fresh
  conversation per invoke with no continuity across invocations.

This keeps the existing spawn-then-invoke workflow working (backwards
compatible) and makes projects **additive**: a project grants multi-turn
context, it does not gate invocation. The greeter agent remains usable
without a project.

# Layer 4 — Execution context seeding

Slice A's `contextStore.init(agentID, nodeID)` (`internal/server/context.go:124`)
gains project/issue args, or a separate `setProject(agentID, project, issue)`
setter called from `SpawnAgent`/`AssignAgent`. `SpawnAgent` is extended to
accept an optional project context (so spawning an agent already assigned to a
project seeds `Project`/`Issue` from the start). `ExecutionContext.Project`
and `.Issue` are no longer "empty until Slice B" — the comments are removed.

The redaction and digest paths (`Redacted()`, `ExecutionContextDigest`)
already carry `Project`/`Issue` and need no change.

# Config

| Key | Default | Description |
| --- | --- | --- |
| `project.workspace_dir` | `.` | Default workspace dir for a project whose create request omits `workspace`. |
| `project.context_retention` | (inherits `agent.context_retention`) | Seconds a finished project's agent contexts are retained before eviction. Defaults to the agent value; override per deployment if project retention should differ. |

No new top-level config section is required unless project-level defaults
grow; for 3.5a a `project.*` group under the existing `Config` (in
`internal/config/horde.go`) follows the `mapstructure` + `HORDE_PROJECT_*`
convention. `cmd/serve.go` maps it into `server.Config`.

# Tests

* **Project store:** create → list → get; state transitions
  (active→paused→active, active→finished); finished project rejects invokes.
* **Team invariants:** creation with no agents rejected; assignment adds to
  team; reassignment moves the agent's active project and updates both teams.
* **One active project:** assigning an agent already active elsewhere
  rebinds it; its `agentProc.activeProject` changes; the old project's team
  no longer lists it.
* **API:** create/list/get/pause/resume/finish/assign/remove via the
  `newTestServer` + `do` pattern (`internal/api/`); assert JSON shapes and
  status codes.
* **Session-key derivation:** invoke an agent with an active project; assert
  the proxied body carries `session_id = "<agent>:<project>"` and that
  `runner.Run` is called with that sessionID (agent-side, via
  `internal/agentapi` tests). Reinvoke the same agent+project and assert
  history continuity (the ADK `InMemoryService` retains it); a different
  project yields a fresh history.
* **No active project (fallback):** invoking an agent with no active project
  omits `session_id` and yields a fresh session per invoke (Phase 3 behaviour)
  — the call succeeds, it does not return an error. Assert no `session_id`
  in the proxied body and that `invID` is used as the `sessionID`.
* **Execution context seeding:** after `SpawnAgent` with a project, the
  context snapshot's `Project`/`Issue` are set (no longer empty — update the
  existing `context_test.go:21-22` assertion).
* **Redaction:** unchanged — `Project`/`Issue` remain remote-visible; no new
  redaction logic. Add a regression test that a finished project's context is
  evicted after retention.

# Open follow-ups (not blocking)

* **Per-user auth and ownership** — 3.5b: `Project.Owner`, per-user
  permission scopes, tool allowlists. The `Team.Users` field is already
  reserved.
* **OS-level filesystem sandboxing** — advisory scope is sufficient for
  3.5a; revisit when the risk model changes with per-user auth.
* **Cross-node project sync** — Phase 4: slaves report their projects to the
  master; a cluster-wide project listing. The digest wire format is already
  project-aware.
* **Agent-to-agent messaging** — deferred per the decision (prior attempt in
  agentd proved challenging).
* **Project lifecycle transitions from `paused`→`finished`** — allow or
  require `active` first; settle when the first user asks for it.
