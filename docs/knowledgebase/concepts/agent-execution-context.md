---
type: Concept
title: Agent execution context
description: The queryable, materialized current-state view of what an agent is working on and whether it can proceed — distinct from process lifecycle and from the transient event stream.
tags: [concept, agents, execution-context, observability]
timestamp: 2026-07-11T00:00:00Z
---

# What it is

**Agent execution context** is a structured, queryable snapshot of an agent's
*work* state at a moment in time: the project and issue it is working, whether
it is blocked, whether it is waiting on the model, recent errors, and pending
approvals. Users and other agents — locally and across the cluster — query it
to answer questions like "is anyone working on issue X?" or "why is that agent
stuck?".

It is deliberately separated from two things it is often confused with:

* **Process lifecycle** (`AgentInfo.Status`: running / exiting / exited, plus
  health) — whether the *process* is alive. An agent can be running-and-healthy
  yet blocked-on-a-decision. Execution context is the second, orthogonal axis.
* **The event stream** — AAP `status` / `error` / `approval_request` / `context`
  frames are transient events. Execution context is the **materialized
  projection** of those events into current state, held by the node and served
  on demand.

# How the fields are sourced (hybrid)

Each field is owned by whoever actually knows it:

| Field | Source |
| --- | --- |
| `project` | host — assigned at launch or on (re)assignment |
| `issue` | host — set with the project; the agent may refine it via AAP `context` |
| `activity` (busy/idle) | agent — AAP `status` |
| `waiting_model` | agent — AAP `context` (capability `execution_context`) |
| `blocked` / `blocked_reason` | agent — AAP `context` |
| `note` (progress) | agent — AAP `context` |
| `errors` | node — aggregated from AAP `error` frames |
| `pending_approvals` | node — it mediates `approval_request`/`approval_response` |
| lifecycle / health | node — process supervision |

# The three layers

Execution context spans the same layering as the rest of the agent stack:

1. **AAP (signal source, local).** The agent self-reports runtime state via the
   additive `context` message (capability `execution_context`); errors and
   pending approvals already flow as AAP frames. See the
   [AAP spec §6.7](/docs/spec/agent-adapter-protocol-v1.md) and the
   [AAP concept](/docs/knowledgebase/concepts/agent-adapter-protocol.md).
2. **Node (materialization + local query).** The node maintains an
   `ExecutionContext` per agent, updated from AAP frames plus launch metadata,
   and serves it over the node API. For native ADK agents (no AAP) the node
   derives only coarse context; the rich fields need an AAP agent — graceful
   degradation by agent kind.
3. **Cluster (cross-node + authorization).** Principals are classified by
   origin (the 3.5a node-granular seam, no per-user auth): a loopback caller is
   `local` and sees full context; a non-loopback caller is `remote`. A remote
   principal sees a **redacted** subset — project, issue, activity, blocked
   (bool), waiting-model, lifecycle, plus **counts** of errors/approvals — but
   not the sensitive detail (blocked reason, note, error text, approval
   payloads, turn id). Redaction is applied at the source (the heartbeat digest
   carries only the subset + counts) and again by the master on read
   (defense-in-depth). The master's aggregated summary is always redacted; a
   node may additionally expose full context to remote callers on **its own**
   endpoints via `agent.context_share = "full"`.

# Status

Built (Slice A). The `ExecutionContext` data model, node-side materialization,
local query API (snapshot + change stream), and cross-node aggregation with
redacted remote access are implemented in `internal/server/context.go` and
`internal/api/context.go`. For native ADK agents (no AAP) the node derives
coarse context (activity + lifecycle); the rich fields (blocked, waiting,
note, errors, approvals) populate when the AAP host feeds frames. The
`Project`/`Issue` fields are empty until Phase 3.5 Slice B (projects/teams)
lands.
