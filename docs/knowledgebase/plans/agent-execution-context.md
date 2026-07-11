---
type: Plan
title: Agent execution context
description: A queryable, materialized per-agent work-state view — sourced hybrid (host + agent + node), served locally, and aggregated across the cluster with read-only, redacted remote access.
tags: [plan, agents, execution-context, cluster, aap]
timestamp: 2026-07-11T00:00:00Z
---

This plan makes an agent's **execution context** — what it is working on and
whether it can proceed — a first-class, queryable thing. See the
[concept](/docs/knowledgebase/concepts/agent-execution-context.md) for the
what/why. This document is the how.

* Signal source: [Agent Adapter Protocol (AAP)](/docs/knowledgebase/decisions/agent-adapter-protocol.md) — the `context` message + `execution_context` capability (already in the spec + `internal/aap`).
* Node/agent mechanism: [Phase 3 — Agent mechanism](/docs/knowledgebase/plans/phase-3-agents.md).
* Topology + authorization context: [master/slave model](/docs/knowledgebase/decisions/master-slave-model.md).

# Scope

**v1 delivers:** the `ExecutionContext` data model, node-side materialization
from AAP frames + launch metadata, a local query API (snapshot + change
stream), cross-node aggregation via the master, and read-only, **redacted**
remote access gated by a minimal node-level principal model.

**v1 does not deliver:** a full user/permission model (only node-level
`local` vs `remote` principals here — the richer model is a separate phase), a
persisted context store (in-memory only; cross-node-durable is Phase 4), or
agent-to-agent queries that bypass the node.

**Depends on:** the AAP `execution_context` extension (done); the Phase 3 agent
mechanism and/or the AAP host to actually run agents; and a minimal
node-authorization seam (introduced here at node granularity).

# Data model

```go
// ExecutionContext is the materialized work-state of one agent.
type ExecutionContext struct {
    AgentID   string
    NodeID    string

    Project   string        // host-assigned at launch
    Issue     string        // host-assigned; agent may refine (AAP context.issue)

    Activity     ActivityState // busy | idle (from AAP status)
    WaitingModel bool          // from AAP context.waiting_model
    Blocked      bool          // from AAP context.blocked
    BlockedReason string       // from AAP context.blocked_reason
    Note         string        // from AAP context.note (progress)

    Errors           []ErrorSummary  // from AAP error frames (recent, bounded)
    PendingApprovals []ApprovalRef    // from the node's approval mediation

    Lifecycle AgentState  // running | exiting | exited (+ unhealthy)
    TurnID    string       // current turn, if any
    UpdatedAt time.Time
}
```

`ErrorSummary` and `ApprovalRef` are bounded, non-sensitive projections (e.g.
error code + truncated message; approval `request_id` + `tool_name`), so that
the redaction policy (below) can expose counts/refs without leaking payloads.

# Layer 1 — AAP (signal source) — done

Delivered additively in the AAP spec and `internal/aap`:

* `context` message (A→H), capability `execution_context`: carries `issue`,
  `blocked`/`blocked_reason`, `waiting_model`, `note` as a partial update.
* `error` frames → `Errors`; the node's `approval_request` mediation →
  `PendingApprovals`; `status` → `Activity`.

Agents without `execution_context` (or native ADK agents with no AAP at all)
yield coarse context only; the node fills what it can observe and leaves the
rich fields zero-valued.

# Layer 2 — Node materialization + local API

## Store

The node keeps an `ExecutionContext` per agent id (a `contextStore` guarded by
the server mutex, or a field on `agentProc`). It is updated from:

1. **Launch** — `SpawnAgent` seeds `Project`/`Issue`/`NodeID`/`AgentID` from the
   spawn request.
2. **AAP frames** — the agent channel's read loop merges `status`, `error`,
   `approval_request`/`approval_response`, and `context` into the store.
3. **Lifecycle** — the existing supervision path sets `Lifecycle` and clears the
   entry (or marks it terminal) on exit.

Each update stamps `UpdatedAt` and notifies change subscribers (for the stream
endpoint).

## API

```
GET /api/v1/agents/{id}/context          → ExecutionContext snapshot (JSON)
GET /api/v1/agents/{id}/context/stream    → SSE of context changes
GET /api/v1/agents/context                → all local agents' contexts
```

The stream reuses the SSE write pattern from `internal/api`; a client watching
an agent gets the current snapshot followed by deltas.

## Degradation by agent kind

* **AAP agents** populate the full model.
* **Native ADK agents** (Phase 3, HTTP/SSE, no AAP): the node derives
  `Activity` from invoke state and `Errors` from the ADK event stream;
  `Blocked`/`WaitingModel`/`Note` stay zero. This is expected and documented,
  not a bug.

# Layer 3 — Cross-node aggregation + authorization

## Aggregation

Slaves report their agents' contexts to the master. Reuse the existing
heartbeat (slave→master) by extending its payload with a **context digest**
(the redacted, remote-visible subset — see below — plus `UpdatedAt`), rather
than adding a new channel. The master keeps an aggregated view keyed by
`(node_id, agent_id)` and evicts entries whose node stops heartbeating.

Sending only the redacted subset over the wire means sensitive fields never
leave the owning node — redaction is enforced at the **source**, not just at the
query edge.

## Remote query API

```
GET /api/v1/cluster/agents/context             → aggregated, redacted contexts
GET /api/v1/cluster/agents/context?issue=proj-42 → filtered (the "who is on X?" query)
```

Served by the master. Read-only: there is **no** cross-node mutation of context
and no way for a remote caller to drive an agent through this surface.

## Authorization + redaction

v1 uses a minimal, node-granular principal model (the full user/permission model
is a separate phase):

| Principal | Sees |
| --- | --- |
| `local` (owner / same node) | full `ExecutionContext` |
| `remote` (another cluster node) | redacted subset: `project`, `issue`, `activity`, `blocked` (bool only), `waiting_model`, `lifecycle`, `updated_at`; **and counts** of errors / pending approvals |

Redacted **out** for remote principals: `blocked_reason`, `note`, error
messages/codes, approval payloads, `turn_id`. Default is restrictive; a node MAY
widen what it shares but MUST NOT exceed the full model. This matches the
[collaboration model](/docs/knowledgebase/decisions/master-slave-model.md): a
coworker can see *that* you are on issue X and blocked, not the sensitive
detail.

# Config

| Key | Default | Description |
| --- | --- | --- |
| `agent.context_retention` | `300` | Seconds a terminal agent's context is retained before eviction. |
| `agent.context_share` | `restricted` | Remote-visible scope: `restricted` (redacted subset) or `full`. |

(Env vars follow the project's config-loader prefix convention; see
`docs/environment.md`.)

# Tests

* **Materialization:** feed a sequence of AAP frames (status, context, error,
  approval) and assert the resulting `ExecutionContext`.
* **Partial-update merge:** successive `context` frames merge (a later frame
  omitting a field leaves the prior value).
* **Local API:** snapshot and stream (initial snapshot + delta).
* **Degradation:** an ADK agent yields coarse context; rich fields zero.
* **Aggregation:** master assembles `(node_id, agent_id)` view from slave
  heartbeats; evicts on heartbeat loss.
* **Redaction:** a `remote` principal never receives `blocked_reason`, `note`,
  error text, or approval payloads — enforced at the source digest.
* **Filter query:** `?issue=` returns only matching agents.

# Open follow-ups (not blocking)

* **Full principal/permission model** — per-user authorization (not just
  node-level `local`/`remote`), a separate phase this hooks into.
* **Persisted / cross-node-durable store** — survive node restart; Phase 4.
* **Agent-to-agent direct queries** — an agent querying another agent's context
  without going through the node API; deferred to the multi-agent context phase.
* **Richer activity** — if busy/idle/waiting proves too coarse, a fuller
  activity taxonomy can be added additively to AAP.
