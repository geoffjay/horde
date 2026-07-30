---
type: Plan
title: Roadmap
description: Phasing of horde capabilities.
tags: [plan, roadmap]
timestamp: 2026-07-08T00:00:00Z
---

# Phase 1 — Foundation (complete)

* CLI with cobra, one file per command.
* Coordinator/worker node modes; `horde serve --mode`.
* Layered configuration system (vendored plantd config).
* logrus logging.
* Hello-world ADK agent (`greeter`) in `agents/`.
* Subprocess agent hosting via `horde agent`.
* TUI (bubbletea + lipgloss).
* Docker integration environment (coordinator + 2 workers).
* Taskfile, GitHub Actions (lint, build, test).
* OKF knowledge base.

# Phase 2 — Server API (complete)

Detailed plan: [Phase 2 — Server API](phase-2-server-api.md).

* Implement the node API transport (the stub previously in `Server.Run`).
* Define the API surface for TUI ↔ server and worker ↔ coordinator.
* Real leader connection / health / registration.

Decisions underpinning this phase:

* [HTTP + SSE transport](/docs/knowledgebase/decisions/http-api-transport.md)
* [TUI consumes the node API](/docs/knowledgebase/decisions/tui-uses-node-api.md)

# Phase 3 — Agent mechanism (complete)

Detailed plan: [Phase 3 — Agent mechanism](phase-3-agents.md).

* Agent subprocess serves a local HTTP API on a unix domain socket.
* Node server reverse-proxies `POST /api/v1/agents/{id}/invoke` to the agent.
* Long-lived agents with concurrent invocations.
* `Last-Event-ID` resume for interrupted SSE streams.
* Real agent registry (`agents.Get(name)`); `--name` selects the agent.
* Structurally real non-LLM agents (streaming, multi-turn within one
  invocation). LLM-backed agents deferred.
* Hung-agent detection via periodic `GET /health` polling.

Decisions underpinning this phase:

* [HTTP over unix domain sockets for agent invocation](/docs/knowledgebase/decisions/agent-invocation-transport.md)

# Phase 3.5 — Multi-agent context (complete)

Decision doc: [Project, team, and user model](/docs/knowledgebase/decisions/project-team-user-model.md).

Built in two slices:

## Slice A — Agent execution context (complete)

Detailed plan: [Agent execution context](agent-execution-context.md).

* `ExecutionContext` data model, node-side materialization from AAP frames +
  launch metadata.
* Local query API (snapshot + change stream).
* Cross-node aggregation via the coordinator with read-only, redacted remote
  access.
* Minimal node-granular principal model (`local` vs `remote`).

Signal fidelity note: full AAP `context`/`error`/`approval` frames arrive with
the AAP host (Phase 3.6, complete); native ADK agents yield only coarse context
(activity + errors). Slice A ships the model, API, and aggregation regardless.

## Slice B — Projects, teams, and multi-turn context (complete)

Detailed plan: [Projects, teams, and multi-turn context](projects-teams.md).

* Projects as a unit of work: workspace path, free-text goal, lifecycle
  states (active/paused/finished).
* Teams of users and agents; agents are peers with no roles; one agent
  active in one project at a time.
* Agent-to-project assignment; session key = `(agent_id, project_id)` for
  private multi-turn context per agent.
* Advisory filesystem scope (no OS-level sandboxing).
* No per-user auth, no tool allowlist, no agent-to-agent messaging.

## Deferred to 3.5b

* Per-user authentication on the node API — **landed in 3.5b** (see below).
* Per-user project ownership and permission scopes — **landed in 3.5b**.
* Per-user tool restrictions — **landed in 3.5b** (the per-user AAP tool
  allowlist).
* OS-level filesystem sandboxing — **still deferred** (per-user filesystem
  scope is advisory, enforced via the AAP tool gate, not at `initialize`).

This split let us build the project/team model and execution context
without committing to an auth mechanism. 3.5b then landed the per-user half —
the project/team model already had the right shape, so it just gained an
`owner` field and access control (see [Phase 3.5b](#phase-35b--per-user-auth-ownership-and-permissions-complete)
below).

# Phase 3.5b — Per-user auth, ownership, and permissions (complete)

Detailed plan: [Phase 3.5b — Per-user auth](phase-3.5b-auth.md).
Decision: [Per-user API-token auth, ownership, and permissions](../decisions/per-user-token-auth.md).

Slice 1 (identity plumbing) is complete: opt-in per-user API-token auth,
`resolvePrincipal` middleware, `GET /users`, `Client.SetAuth`, the TUI Users
view, and the `X-Horde-User` cross-node echo. Slice 2 (ownership + project
authz) is complete: projects record an `Owner`, `authorizeProject` (owner +
team members) gates project mutations, `requireUser` rejects anonymous
mutations, and team membership endpoints (`POST/DELETE /projects/{id}/users`)
are wired with raft-replicated `AddUser`/`RemoveUser` ops. Slice 3 (agent
mutation gating) is complete: `requireUser` guards `POST /agents`, `DELETE
/agents/{id}`, `POST /agents/{id}/invoke`, and
`POST /agents/{id}/approvals/{requestID}` — anonymous mutations are rejected
at the edge (401), node principals pass, and disabled-by-default is a no-op.
Slice 4 (AAP tool allowlist + advisory scope) is complete: the invoke path
resolves a per-user `AAPUserScope` from the request principal (re-derived
from local config on a cross-node forward via `X-Horde-User`), threads it
through `AAPInvoke` → `runAAPTurn` → `sendPrompt`, and the host session's
`resolveApproval` denies any tool not in the user's `AllowedTools`
(advisory: the per-user filesystem scope is enforced via the tool gate, not
`initialize.permissions`, since the adapter is created before any user is
known). Slice 5 (docs/KB) is complete: the
[per-user-token-auth decision](../decisions/per-user-token-auth.md), the
[principal-middleware-and-echo-trust pattern](../patterns/principal-middleware-and-echo-trust.md),
and the 3.5b half of the
[project/team/user model decision](../decisions/project-team-user-model.md)
are recorded; `docs/environment.md` and `concepts/environment.md` already
carried the `auth.users` block, `HORDE_USER_TOKEN`, and `--token` from
slice 1. The optional `invoked_by` attribution field was not added (the
invoke path has no logging today and the field was explicitly optional).

**Phase 3.5b complete.** All five slices have landed; per-user auth,
ownership, and permissions are available as an opt-in mode.

# Phase 3.6 — AAP host (external coding agents) (complete)

Detailed plan: [AAP host — driving external coding agents](aap-host.md).

Decision: [Adopt the Agent Adapter Protocol (AAP)](/docs/knowledgebase/decisions/agent-adapter-protocol.md).
Spec: [Agent Adapter Protocol v1](/docs/spec/agent-adapter-protocol-v1.md).

The product path: drive **external** AI coding agents (Claude Code and others)
through AAP adapters — "coding, but for documents" over the OKF knowledge base.
Phases 3–3.5 build the mechanism and the project/team scaffolding on native ADK
agents; this phase is where real coding agents plug in.

* Node spawns AAP adapters over the stdio binding (NDJSON): the
  `initialize`→`ready` handshake, the prompt/turn loop, and graceful shutdown.
* A second agent *kind* alongside native ADK: AAP agents are declared in
  config (`agents.<name>.kind: aap`) rather than registry-built; both kinds
  share the `agentProc` map, the invoke API, and project assignment.
* Tool approval wired to node policy (the node is the sole approval
  authority); the project workspace mapped onto AAP `workspace.cwd` +
  `initialize.permissions`.
* Consume AAP `context`/`error`/`approval_request` frames to populate the
  [agent execution context](agent-execution-context.md) at full fidelity —
  this lights up the `applyStatus`/`applyContextUpdate`/`applyError`/
  `applyApprovalRequest` receivers Slice A left waiting.
* The `horde aap-mock` fixture driven end to end as the first adapter. The
  first real adapter — **pi-aap** (for the `pi` coding agent) — is now wired
  and handshake-verified through the host (`TestSpawnAAPAgent_PiAdapter`,
  opt-in via `HORDE_TEST_PI_ADAPTER`); a live turn against a model is verified
  manually. See [`docs/examples/pi-agent.yaml`](/docs/examples/pi-agent.yaml).

Independent of per-user auth (3.5b): can land before or after it. Foundation
already in place — the AAP spec and the `internal/aap` package (typed messages,
  mock adapter, shared test vectors).

# Phase 4 — Distributed

Detailed plan: [Phase 4 — Distributed](phase-4-distributed.md). Built in slices.

* Worker registration with the coordinator. ✅ (Phase 3.5a + slice 1 hardening:
  routable advertised address, stale-worker eviction.)
* Agent placement and coordination across nodes. **Slices 1–2 done**: the
  coordinator routes an invoke to whichever node hosts the agent (slice 1,
  cross-node invoke via a reachable advertised address) and can place a new
  agent on a chosen node — an explicit worker, or `auto` (least-loaded) — via
  `POST /api/v1/agents` with a `node` field (slice 2, spawn forwarding).
* Cluster discovery beyond `static`. **Slice 3 done**: a worker can find its
  leader via `discovery_mechanism: dns` (an SRV lookup of
  `cluster.discovery_dns_name`, re-resolved each reconnect) instead of a
  hardcoded `server.leader`. Gossip discovery is a later slice.
* Cross-node event fan-out. **Slice 4 done**: the previously-unused in-process
  `EventBus` now carries agent lifecycle events (`agent.spawned`/`exiting`/
  `exited`), streamed over `GET /api/v1/events/stream` (SSE). Workers push their
  events to the coordinator (`POST /api/v1/cluster/events`), which republishes them,
  so the coordinator's stream is a cluster-wide feed.
* Gossip discovery. **Slice 5 done**: the third `discovery_mechanism` —
  `gossip` — has workers find the coordinator through a `hashicorp/memberlist` (SWIM)
  ring, where the coordinator advertises itself; no per-worker leader address. This
  completes Phase 4. Automatic leader *failover* is deferred (see the
  [cluster failover](../concepts/cluster-failover.md) concept doc).
* Phase 4 hardening / surfacing. **Done**: cluster request auth (shared bearer
  token `cluster.auth_token`) + gossip wire encryption
  (`cluster.gossip_encryption_key`); any node is a valid invoke entry point
  (a worker forwards an unknown-agent invoke to the coordinator); and the TUI/client
  surfaces for placement (a new-agent form with a node picker) and the event
  feed (a live cluster-activity view). mTLS is the intended long-term node auth
  (see the [cluster mTLS](../concepts/cluster-mtls.md) concept doc).

# Phase 5 — Leader failover

Detailed plan: [Leader failover](leader-failover.md). Decision:
[Raft for leader election and coordinator-state replication](/docs/knowledgebase/decisions/raft-leader-election.md).
Built in slices.

Phase 4 leaves a statically designated, single-point-of-failure coordinator. Phase 5
makes leadership *survive* the loss of a node: opt-in **raft** election
(`cluster.failover: raft`) layered on the gossip ring, with coordinator-only state
(the project store and AAP resume tokens) replicated through the raft log so an
elected leader comes up current. Default (static-coordinator) behaviour is unchanged.

* Slice 1 — raft membership + election (leader lookup via a `raftDiscoverer`;
  no state replication yet). **Done**: a raft quorum over the gossip ring elects
  the leader, role is dynamic (`Server.isCoordinator()`), and a follower re-targets
  the new leader after an election.
* Slice 2 — replicate the project store through the raft log (an FSM).
  **Done**: `raftProjectStore` routes project mutations through `raft.Apply`
  (deterministic replay), so a newly-elected leader has current project state.
* Slice 3 — replicate AAP resume tokens. **Done**: resume tokens replicate
  through the raft log (cluster-global by agent name), so a respawn on a new
  leader resumes; follower-hosted agents keep node-local resume (the boundary).
* Slice 4 — a stable client/TUI entry point that follows the leader across a
  failover. **Done**: the client holds a member set (learned from `ListNodes`)
  and rotates to a survivor on a transport failure; the TUI benefits
  automatically and already surfaces the leader. VIP/DNS is the ops alternative.

**Phase 5 complete.** All four slices have landed; automatic raft leader failover
is available as an opt-in mode. Requirements background: the
[cluster leader failover](../concepts/cluster-failover.md) concept doc.

# Phase 6 — Knowledgebase sync (the distributed shared brain) (complete)

Detailed plan: [Knowledgebase sync](knowledgebase-sync.md).
Spec: [Knowledgebase Sync Protocol v1](/docs/spec/knowledgebase-sync-protocol-v1.md).
Decision: [Knowledgebase sync — authority-serialized multi-writer](../decisions/knowledgebase-sync.md).

horde's central differentiator: every project has a per-project OKF
knowledgebase, and this phase makes it a **cluster-shared** brain rather than a
purely local tree. Today `scaffoldKnowledgebase`
(`internal/server/knowledgebase.go`) seeds a local `.horde/knowledgebase/` per
project, but nothing watches or replicates its *files*; the cluster replicates
only project/team metadata + AAP resume tokens (raft), never file content. The
[persistence-and-knowledgebase decision](../decisions/persistence-and-knowledgebase.md)
§4 named KB sync "the hardest problem" and deferred it — this phase builds it.

* Goal: **symmetric multi-writer** — every participating node watches its own
  `.horde/knowledgebase/`, and an edit on *any* node propagates to the others.
  **Achieved in stage 2**: a participant with `watch_local: true` watches its
  local tree and pushes local edits via CAS; a 412 conflict preserves the
  local content to the conflict area before converging to canonical.
* Opt-in `knowledgebase.sync` config; disabled ⇒ byte-for-byte current (local,
  git-backed) behavior.
* **Scope-parameterized, project scope only.** The replicated unit is a scope
  `{kind, id}` — a label on the manifest and a key in the route
  (`/api/v1/kb/{kind}/{id}/…`), never an input to convergence. Only `project` is
  registered here; `team`, `user`, and `cluster` are reserved so each becomes a
  later *registration* (four bindings: identity, authority, location,
  authorization) rather than a protocol revision or a route migration. `team` is
  blocked on making teams first-class entities (`Team` is a struct inside a
  project today), `user` on cluster-consistent identity plus selective
  participation; `cluster` is the cheap one. How a consumer composes a single
  view across scopes is an open question, deliberately outside the protocol.
* **Authority-serialized writes, content-digest identity, three-way
  convergence, compare-and-swap.** Digests are authority-independent, so a
  leader change cannot regress ordering (no version counters, no terms); the
  manifest is complete rather than incremental (a node that misses any number of
  updates converges on its next poll); each node tracks `synced_digest`
  separately from disk, which is how it tells "changed because I pulled it" from
  "changed because a user edited it" — the pivot that makes multi-writer safe;
  deletion is absence from the manifest (no tombstones); a write conflict is an
  explicit `412`, never a silent merge or an arbitrary tiebreak.
* Delivered in **two stages on one wire protocol** — a stage-2 watcher calls the
  same CAS endpoint a stage-1 API client calls. *Stage 1* (complete): (1) the
  scope seam + authority manifest + read API; (2) authority watcher; (3)
  participant convergence — the KB becomes shared across hosts; (4) CAS writes
  from any node. *Stage 2* (complete): (5) participant watcher + push — local
  file edits propagate, the goal; (6) offline replay via persisted sync records
  + conflict area with operator surfacing (`GET …/conflicts`). Then (7) docs/KB.
* **Pull, not event-push**, deliberately: the event bus fans *in* not out
  (`forwardEvents` is worker→coordinator; there is no outbound push), it drops on slow
  subscribers by design, and `server.Event` is a closed struct whose "carries no
  sensitive payload" comment is load-bearing. Polling keeps every call in the
  node→leader direction the codebase already supports.
* The one limitation that persists past stage 2: simultaneous edits to the *same
  file* cannot be auto-merged — the loser gets a preserved copy in a conflict
  area outside the tree. That is intrinsic to file-granular sync; only
  structured/CRDT merge avoids it.

**Phase 6 complete.** Both stages have landed; symmetric multi-writer
knowledgebase sync is available as an opt-in mode. The KSP v1 spec is finalized.
Not blocked by (and does not block) mTLS or OS-level sandboxing; it does unblock
external agents *participating in* a shared knowledgebase.
