---
type: Plan
title: TUI for projects, teams, and execution context
description: Mockup concepts and an implementation sketch for growing the TUI from a single node/agent screen into a breadcrumb-navigated app over projects, teams, per-agent execution context, multi-turn invoke, and cluster topology.
tags: [plan, tui, bubbletea, lipgloss, projects, teams, execution-context, phase-3.5]
timestamp: 2026-07-13T00:00:00Z
---

This plan proposed how the TUI (`internal/app`) surfaces the domain that
[Phase 3.5 Slice B](projects-teams.md) opened up — projects, teams, and
multi-turn context — plus the [Slice A execution
context](agent-execution-context.md) and the cluster topology from
[Phase 2](phase-2-server-api.md). It was a **design/mockup** doc; the screens
and navigation were proposals to react to, not a settled contract.

**Status: complete.** All eight slices landed on 2026-07-15: (1) the
`internal/client` HTTP layer, (2) the breadcrumb navigation model, (3) the
view-aware status-line hint blocks, (4) the projects home and project detail
screens, (5) the live agent execution-context pane (SSE), (6) the multi-turn
invoke/conversation view (SSE), (7) the cluster topology view, and (8) the
palette lifecycle commands plus the new-project form modal. The TUI remains a
pure client of the node API (bubbletea/lipgloss v2, no `internal/server`
imports) and passes `go test -race`, `golangci-lint` (0 issues), and
`go build`. The follow-ups below (approval decisions, per-user identity,
cross-node project listing) remain out of scope and deferred to their
respective phases.

* Slice B (the data): [Projects, teams, and multi-turn context](projects-teams.md).
* Slice A (the observability): [Agent execution context](agent-execution-context.md).
* Existing TUI conventions this design extends: [TUI status line and command palette](/docs/knowledgebase/patterns/tui-status-line-and-palette.md).
* Constraint: [The TUI consumes the node API](/docs/knowledgebase/decisions/tui-uses-node-api.md) — every screen below reads the node HTTP API; none reach into `internal/server`.

# Where the TUI is today

`internal/app` is a single screen: the bold pink `horde` title, a faint
`mode:` line (plus `• leader connected`), and a flat "Running agents:" list —
or the disconnected 60s-retry panel. Below it, the right-aligned status line
(`nodeStatusBlock` + `commandsBlock`) and the `ctrl+p` command palette
(three commands: Refresh/Retry, Quit) over a dimmed background. It consumes
only `/health`, `/node`, `/agents`. The `Model` has no notion of projects,
execution context, or cluster nodes.

# What the API already exposes (untapped)

| Data | Endpoint(s) | Key fields |
| --- | --- | --- |
| Projects | `GET/POST /projects`, `…/{id}`, `…/pause` `…/resume` `…/finish` `…/agents` | id, name, workspace, goal, state, team.agents (agent_id, name, assigned_at) |
| Execution context | `GET /agents/context`, `…/{id}/context`, `…/{id}/context/stream` (SSE) | project, issue, activity (busy/idle), waiting_model, blocked+reason, note, errors (code/message/fatal), pending_approvals (request_id/tool_name), lifecycle, turn_id, updated_at |
| Invoke | `POST /agents/{id}/invoke` (proxied SSE) | message in; SSE stream out; 409 when project paused/finished |
| Cluster | `GET /cluster/nodes`, `GET /cluster/agents/context` | leader_id; per node: node_id, addr, agents, last_seen, stale; remote contexts are **redacted** (counts, not error/approval detail) |

# Navigation model

Keep the single-window frame (top-flush pink title, one-cell edge inset,
right-aligned status line, alt-screen). Grow from one screen to a
**breadcrumb drill-down stack**:

```
projects → project → agent → invoke
cluster  → node
```

* The `ctrl+p` palette is the primary navigator — it gains `Go to Projects`,
  `Go to Cluster`, and context actions (new project, assign, pause/finish,
  invoke, approve/deny).
* Within list views: `↑↓` select, `enter` drills in, `esc` pops back.
* A breadcrumb line (`projects › auth-service › reviewer`) replaces today's
  `mode:` line; node mode/leader move fully into the status line.
* Projects become the **home** screen; agents are reached through a project or
  the cluster view.

Color legend (lipgloss renders these; ASCII cannot): `horde` bold pink(212);
faint = grey; `●` green(42) = active/idle; `◐` yellow = busy/paused/waiting;
`▲` red(203) = blocked/error; `○` grey = finished/exited.

# Screens

## 1 — Projects home (new default connected view)

```
horde
projects

  ●  auth-service       active     3 agents   Fix flaky login and add MFA
  ◐  billing-rewrite    paused     2 agents   Migrate to the Stripe billing API
  ○  docs-refresh       finished   1 agent    Rewrite the getting-started guide

  4 agents · 1 idle · 2 busy · 1 blocked


                    ● master · n1 · 4 agents  ›  ↑↓ select · enter open · n new  ›  ctrl+p commands
```

Feeds: `GET /projects` (name, state, team.agents, goal); rollup from
`GET /agents/context` activity states.

## 2 — Project detail

```
horde
projects › auth-service

  active   workspace ~/work/auth-service
  goal     Fix flaky login and add multi-factor auth

  Team
    ● greeter    idle      #142 login-timeout    turn 3
    ◐ coder      busy      #142 login-timeout    ⋯ running tests
    ▲ reviewer   blocked   #150 mfa-design       2 errors · 1 approval


          ● master · n1 · 4 agents  ›  enter invoke · a assign · p pause · f finish  ›  ctrl+p commands
```

Feeds: `GET /projects/{id}` + per-agent `GET /agents/context`. Actions map to
`/pause` `/finish` `/agents`.

## 3 — Agent execution context (live SSE)

```
horde
projects › auth-service › reviewer

  reviewer  [a3f9c1]     ▲ blocked          lifecycle running · turn t-88
  project   auth-service · issue #150 mfa-design
  activity  idle · waiting on model: no
  blocked   awaiting tool approval

  Pending approvals (1)
    ▸ write_file          req 7c2e…      enter approve   x deny

  Errors (2)
    ✗ E_TOOL    write_file: permission denied to /etc/hosts       fatal
    ✗ E_MODEL   rate limited, backing off (retry 2)

  note  paused pending human review of MFA design

  live ●                             ● master · n1 · 4 agents  ›  esc back  ›  ctrl+p commands
```

Feeds: `GET /agents/{id}/context/stream` (SSE `event: context`, snapshot then
deltas). `live ●` is a status-line block indicating the stream is connected.
(Approve/deny is now wired: with an AAP agent and `auto_approve: false`, the
pane lists pending approvals; `↑↓` selects and `a`/`d` allow/deny via
`POST /api/v1/agents/{id}/approvals/{requestID}`. For agents without pending
approvals the pane is read-only.)

## 4 — Invoke / conversation (multi-turn)

```
horde
projects › auth-service › coder › invoke

  session coder:auth-service · multi-turn

  › you
    add a rate limiter to the login handler

  ● coder
    I'll add a token-bucket limiter. Editing internal/auth/login.go…
    ▸ edit login.go  (+24 −3)
    Done — tests pass. Want me to wire the config key too?

  ›_ type a message                                          streaming ●


                              ● master · n1 · 4 agents  ›  enter send · esc back  ›  ctrl+p commands
```

Feeds: `POST /agents/{id}/invoke` (proxied SSE). The session banner reflects the
`agent:project` key the node derives. A `409` (project paused/finished) renders
inline as a red notice in place of the input box.

## 5 — Cluster

```
horde
cluster

  leader  n1  (this node)

  ● n1   127.0.0.1:8080   master   4 agents   seen just now
  ● n2   10.0.0.12:8080   slave    2 agents   seen 3s ago
  ◐ n3   10.0.0.13:8080   slave    0 agents   seen 41s ago   stale

  remote agents (read-only)
    ● n2 · packager    idle      billing-rewrite  #88
    ▲ n2 · deployer    blocked   billing-rewrite  #90   1 approval


                                    ● master · n1 · 4 agents  ›  enter node  ›  ctrl+p commands
```

Feeds: `GET /cluster/nodes` (node_id, addr, agents, last_seen, stale) +
`GET /cluster/agents/context` (redacted — counts, not detail).

## 6 — Command palette, evolved

```
      ╭──────────────────────────────────────────────╮
      │  Commands                                esc  │
      │                                               │
      │  pro_                                         │
      │                                               │
      │  Go to Projects                          g p  │
      │  New project…                            n    │
      │  Assign agent to project…                a    │
      │  Pause project                           p    │
      │  ↓ more                                       │
      ╰──────────────────────────────────────────────╯
```

Same rounded box / dimmed background / windowed rows as today (see the
[palette pattern](/docs/knowledgebase/patterns/tui-status-line-and-palette.md));
just a richer, view-aware `commands()` set.

## 7 — New-project form (modal, same overlay mechanism)

```
      ╭─ New project ────────────────────────────────╮
      │                                               │
      │  Name       auth-service_                     │
      │  Workspace  ~/work/auth-service               │
      │  Goal       Fix flaky login and add MFA       │
      │  Agents     greeter, coder, reviewer          │
      │                                               │
      │              enter create · esc cancel        │
      ╰───────────────────────────────────────────────╯
```

Feeds: `POST /projects` (`createProjectRequest`: name, workspace, goal,
agents[]).

The disconnected / 60s-retry panel and the `paint`-based dimming are unchanged.

# Implementation sketch

* **`Model`** gains `view` (enum `projects · projectDetail · agent · invoke ·
  cluster`), a breadcrumb/selection stack, and cached slices: `projects`,
  `contexts map[string]ExecutionContext`, `nodes`, and an invoke transcript
  buffer.
* **`internal/client`** gains `ListProjects/GetProject/CreateProject/Pause/
  Resume/Finish/AssignAgent`, `StreamContext` (SSE), `ListNodes`, and `Invoke`
  — mirroring the DTOs above.
* **Messages/commands**: `projectsMsg`, `clusterMsg`, `contextDeltaMsg` (SSE),
  `invokeChunkMsg`. The existing 2s tick stays for list refresh; context and
  invoke use SSE subscriptions rather than polling.
* **Status line**: add a `breadcrumb` block and a per-view `hint` block (the
  mid-line `enter/esc/a/p` hints) — both are ordinary removable `StatusBlock`s
  per the pattern doc.
* **Palette**: `commands()` becomes view-aware (navigation + lifecycle +
  assign/remove + invoke).

# Decided (flag to revisit)

* **Breadcrumb drill-down** over a persistent left sidebar — fits the current
  top-flush frame with the least layout surgery.
* **Projects as home** — agents are reached through a project or the cluster
  view, matching the Slice B framing of the project as the unit of work.

# Out of scope / follow-ups (not landed)

Delivered as a follow-up after the AAP host landed:

* Approval **decisions** (approve/deny wiring). ✅ The agent-context pane
  selects a pending approval (`↑↓`) and decides with `a`/`d` against
  `POST /api/v1/agents/{id}/approvals/{requestID}`.

These remain deferred:

* Per-user identity in the UI (whose project, who is invoking) is 3.5b.
* Cross-node project listing (the cluster view shows remote *agents*; a
  cluster-wide *project* surface arrives with Phase 4 sync).
</content>
</invoke>
