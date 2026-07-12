---
type: Decision
title: Project, team, and user model
description: What a project, team, and user are in horde; how they relate; and the 3.5a/3.5b split that defers per-user auth.
tags: [decision, architecture, agents, projects, teams, users, phase-3.5]
timestamp: 2026-07-11T00:00:00Z
---

# Context

Phase 3 delivered the agent mechanism — long-lived agent subprocesses invoked
over HTTP on unix sockets, with streaming, resume, and a real agent registry.
The roadmap's Phase 3.5 introduces the concepts that make horde usable for
real work: projects, teams, permissions, and multi-turn context. These
concepts constrain the agent invocation contract (the session key, the
workspace path, the invoke payload), so they must be settled before the plan
doc.

This decision was resolved through a structured conversation with seven
questions. Each is recorded below with its outcome.

See:
* [Phase 3 — Agent mechanism](/docs/knowledgebase/plans/phase-3-agents.md)
* [Agent execution context](/docs/knowledgebase/concepts/agent-execution-context.md)
* [Master/slave cluster model](/docs/knowledgebase/decisions/master-slave-model.md)

# 1. What is a project?

A project is the unit of work. It has:

* **`id`** — unique identifier.
* **`name`** — human-readable.
* **`workspace`** — a filesystem directory path. This is the filesystem
  boundary for the project's agents. It does not require a git repo, though
  a workspace can certainly be a git repo.
* **`goal`** — a free-text description of what the team is working toward.
  No structured fields (issue tracker links, milestones, deadlines) in 3.5a;
  those can be added later without changing the model.
* **`state`** — lifecycle: `active` | `paused` | `finished`.
  * `active` — agents can be invoked.
  * `paused` — agents cannot be invoked; the project is temporarily
    suspended.
  * `finished` — the project is done; its agents are released and its
    context is retained for a configurable period before eviction.
* **`team`** — the set of users and agents assigned to the project (see
  below).

Projects are owned by the node in 3.5a (no per-user ownership). Per-user
ownership comes with 3.5b.

# 2. What is a team?

A team is the set of **users and agents** assigned to a project — not just
agents. A team is never empty (it always has at least one member, user or
agent).

## Membership

* **Agents** are assigned at project creation and can be added/removed
  later (static-ish but mutable).
* **Users** join by permission, by request, or by invitation. For a local
  single-node system there are zero or more users in a team. For a
  multi-node system, a second user joining as a slave node can see projects
  and join if already permitted, request to join, or be requested to join.
  This is the cluster membership model; it builds on the
  [master/slave model](/docs/knowledgebase/decisions/master-slave-model.md)
  and is fleshed out in Phase 4.

## Roles

No roles in 3.5a. All agents on a team are peers. Agents have their own
purpose (determined by which agent they are — greeter, repeater, a future
LLM-backed agent), and restricting by role is unnecessary. Roles (lead,
reviewer, worker) can be added later if coordination demands them.

## One agent, multiple projects

An agent subprocess can participate in more than one project over its
lifetime, but it can only be **active in one project at a time**. This
differs from the Phase 3 model where an agent subprocess is spawned for a
single purpose. In 3.5a, spawning an agent (or assigning an existing agent
to a project) sets its active project; invoking the agent operates within
that project's context. Reassigning an agent to a different project changes
its active project and its session key (see multi-turn context below).

## Agent-to-agent messaging

Deferred. A team is a set of agents working on the same project, but agents
do not communicate with each other directly in 3.5a. The user coordinates by
invoking each agent separately. Agent-to-agent messaging was attempted in a
prior project (agentd) and proved challenging; it is deferred here.

# 3. What is a user?

Phase 3.5 is split into two slices:

## 3.5a — No per-user auth

* The node API remains unauthenticated.
* Projects are owned by the node, not by a user.
* A minimal **node-granular principal model** (`local` vs `remote`) is
  introduced as the authorization seam for the
  [agent execution context](/docs/knowledgebase/concepts/agent-execution-context.md).
  `local` = owner / same node; `remote` = another cluster node.
* This is enough to build the project/team model, multi-turn context, and
  execution context without committing to an auth mechanism.

## 3.5b — Per-user auth (later)

* Per-user authentication on the node API.
* Per-user project ownership (the `owner` field on a project).
* Per-user permission scopes (tool restrictions, workspace access).
* The full user/permission model that the execution context plan references
  as "a separate phase."

This split lets us build the project/team model and the execution context
without choosing an auth mechanism (API keys, JWT, OAuth, etc.). When 3.5b
lands, the project/team model already has the right shape — it just gains
an `owner` field and access control.

# 4. Permissions

For 3.5a:

* **Filesystem scope** = project workspace. The node passes the workspace
  path to the agent subprocess at spawn/assignment time. Agents operate
  within it **by convention** — no OS-level enforcement (no chroot, seatbelt,
  or landlock). Enforcement is a much larger lift and is not needed in 3.5a
  where there is no per-user auth.
* **Tool allowlist** = deferred. All agents on a project have the same tool
  access. Per-user tool restrictions come with 3.5b.
* **Agent-to-agent messaging** = deferred (see above).

OS-level sandboxing can be revisited when per-user auth lands (3.5b) and
the risk model changes. For now, advisory scope is sufficient — the agent
is told the workspace path and operates within it.

When external coding agents arrive via the [AAP host (Phase 3.6)](/docs/knowledgebase/plans/roadmap.md),
this workspace maps onto AAP's `workspace.cwd` and the optional
`initialize.permissions` scope — the same advisory, adapter-self-enforced
model. The two permission surfaces stay unified rather than diverging.

# 5. Multi-turn context

Phase 3 uses a fresh `sessionID` per invocation (derived from the
invocation id). The `runner.Runner` + `session.InMemoryService()` already
support conversation history when the same `sessionID` is reused across
`/invoke` calls.

**Decision:** the session key is `(agent_id, project_id)`. Each agent has
its own **private** conversation history scoped to the project. Agents on
the same team do not share conversation history — they each have their own.

This gives:
* Multi-turn context per agent within a project (an agent remembers prior
  turns).
* No cross-agent conversation leakage (agents don't see each other's
  history).
* No new mechanism — just a stable session key passed through the existing
  invoke contract.

The node derives `sessionID` from `(agent_id, project_id)` and passes it in
the invoke request body to the agent subprocess. The agent subprocess uses
it as the `sessionID` for `runner.Run`.

# 6. Invocation payload shape

The project is **implicit**. When an agent is spawned or assigned to a
project, its active project is set. The invoke call does not carry the
project — it is already known.

```
POST /api/v1/agents/{id}/invoke
{
  "message": "fix the bug in auth.go"
}
```

The node derives the `session_id` from `(agent_id, project_id)` and passes
it in the invoke request body to the agent subprocess, alongside the Phase 3
`invocation_id`:

```json
{
  "message": "fix the bug in auth.go",
  "session_id": "derived-from-agent+project",
  "invocation_id": "uuid-optional"
}
```

`session_id` and `invocation_id` are distinct and **coexist**:

* `session_id` = `(agent_id, project_id)` — conversation continuity *across*
  invocations (the `sessionID` for `runner.Run`).
* `invocation_id` — identifies *one* `/invoke` call, driving the Phase 3
  `Last-Event-ID` resume broker. Unchanged from Phase 3.

The `userID` passed to `runner.Run` stays a fixed value (`"local"`) in 3.5a;
per-user identity is 3.5b. No URL change — the existing
`POST /api/v1/agents/{id}/invoke` endpoint is unchanged. This keeps the API
simple and matches the Phase 3 shape.

# 7. Sequencing

3.5a is built in two slices:

1. **Slice A — Agent execution context.** Already planned in detail in
   [plans/agent-execution-context.md](/docs/knowledgebase/plans/agent-execution-context.md).
   Depends only on the `local`/`remote` principal seam, which is minimal.
   Can be built immediately — it does not depend on the project/team model.

2. **Slice B — Projects, teams, and multi-turn context.** Depends on this
   decision doc. Introduces the project/team data model, the project API
   (create/list/pause/finish), agent-to-project assignment, and the
   session-key derivation for multi-turn context.

Building slice A first means the execution context (queryable per-agent
work-state) is available before projects/teams land, giving observability
into agents from the start. Slice B then adds the project/team structure
on top.

# Consequences

* A project is the unit of work with a workspace, a free-text goal, and
  lifecycle states.
* A team includes both users and agents; agents are peers with no roles.
* An agent can participate in multiple projects but is active in one at a
  time.
* No per-user auth in 3.5a — the node API stays unauthenticated. Per-user
  auth, ownership, and permission scopes are 3.5b.
* Filesystem scope is advisory (no OS-level sandboxing in 3.5a).
* Multi-turn context uses a private session per `(agent_id, project_id)`.
* The invocation payload is unchanged from Phase 3 (project implicit at
  spawn; session_id derived by the node).
* Agent-to-agent messaging is deferred.
* The agent execution context (slice A) can be built first, independent of
  the project/team model.
