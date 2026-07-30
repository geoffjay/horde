---
type: Plan
title: Knowledgebase sync — the distributed shared brain
description: Plan for sharing each project's per-project OKF knowledgebase across the cluster, reaching symmetric multi-writer (every node's tree watched) in two stages — an authority-serialized canonical tree, content-digest identity, three-way convergence, and compare-and-swap writes. Scope-parameterized so team/user/cluster knowledgebases are later extensions, not a protocol revision; project scope ships first. Opt-in and backward-compatible; the wire format is KSP v1.
tags: [plan, knowledgebase, sync, distributed, cluster, projects]
timestamp: 2026-07-28T00:00:00Z
---

> **Status: stage 2 complete (slices 1–6).** Roadmap phase 6. This builds
> horde's central differentiator — the per-project knowledgebase as a
> *cluster-shared* brain — which the
> [persistence-and-knowledgebase decision](../decisions/persistence-and-knowledgebase.md)
> §4 named "the hardest problem" and deferred ("Slice B creates the KB but does
> not sync it"). The wire format is
> [Knowledgebase Sync Protocol v1](/docs/spec/knowledgebase-sync-protocol-v1.md).

# Goal

**Symmetric multi-writer.** Every participating node watches its own
`.horde/knowledgebase/` tree; a user or agent editing a file on *any* node has
that change propagate to all the others. One shared, live document brain.

Delivered in two stages. Stage 1 makes the knowledgebase shared, readable, and
writable-by-API on every node; stage 2 turns on each node's watcher so local
file edits propagate. **The wire protocol is the same for both** — a stage-2
watcher calls the identical CAS write endpoint a stage-1 API client calls — so
stage 1 is genuinely stage one of the destination, not a detour.

**Scoped, and parameterized on scope.** The long-term intent is knowledgebases
at several levels — a user's own, a project's, a team's tribal knowledge, the
cluster's organizational knowledge. Only the **project** scope ships here, but
the protocol and the code are built around a scope key from day one (KSP §2.1,
§12) so a further scope is a registration, not a protocol revision or a route
migration. What is *not* built now is any of the other three: see
[Future scopes](#future-scopes) for what each is actually blocked on.

# Context

Every project gets a local `.horde/knowledgebase/` OKF tree, scaffolded at
creation by `scaffoldKnowledgebase` (`internal/server/knowledgebase.go`),
non-destructively via `writeIfNotExists` (`knowledgebase.go:139-144`). That tree
is **local only** — the code says so at `knowledgebase.go:87-89`: *"This does not
implement synchronization — that is a future phase … travels with it (e.g.
checked into git)."*

The cluster replicates **metadata only**: the raft FSM carries project/team
metadata and AAP resume tokens (`raftstate.go:34-38`, `raftproject.go:26-38`);
`Project.Workspace` (`project.go:49`) is a **path string**, never content.
Cross-node machinery moves events (`forwardEvents`, `server.go:687`),
project-API requests (`ForwardProjectRequest`, `server.go:1136`), and invoke
routing (`RemoteAgentNode`, `server.go:1338`) — never file bytes. There is no
file watcher (fsnotify is an *indirect* dep only).

An earlier draft proposed a bidirectional, optimistically-versioned, event-push
design. Review found it unsound — lost updates on apply, offline edits that
could never propagate, version regression across failover, unreachable tiebreak
rules, and a tombstone black hole that swallowed recreates. It was replaced with
the model below, which reaches the same destination on foundations that hold.

# Design

**Authority-serialized writes; content-digest identity; three-way convergence;
compare-and-swap.** See [KSP v1](/docs/spec/knowledgebase-sync-protocol-v1.md).

```
authority for scope {project, P}          participant node
 <workspace>/.horde/knowledgebase/         <local workspace>/.horde/knowledgebase/
        │  watch (fsnotify)                       │  watch (stage 2)
        ▼         /api/v1/kb/{kind}/{id}/…        ▼
   canonical manifest ◀── GET manifest (If-None-Match) ── poll + converge
   (path → digest)    ◀── PUT/DELETE (If-Match CAS) ────── writes, both stages
```

Six properties do the work:

- **The replicated unit is a *scope*, not a project.** A scope is `{kind, id}`
  (KSP §2.1) — a label on the manifest and a key in the route, never an input to
  convergence. `project` is the only kind registered; the algorithm beneath it is
  identical for any other. Two scopes are two independent instances: separate
  manifests, sync records, trees, authority resolution, and authorization.

- **Identity is the content digest**, not an assigned version. Digests are
  authority-independent, so a leader change cannot regress or corrupt ordering —
  no counters, no terms, no manifest hand-off.
- **The manifest is complete**, not incremental. A node that misses any number
  of updates converges on its next poll. No event stream to miss, no gap
  detection, no acknowledgement tracking.
- **`synced_digest` is tracked separately from disk** (KSP §2.4). Comparing it
  to the on-disk digest is how a node tells *"changed because I pulled it"* from
  *"changed because a user edited it"* — the distinction that makes multi-writer
  safe and whose absence causes silent lost updates. **Maintained from stage 1**,
  where it also lets a not-yet-writing node preserve an unexpected local edit
  instead of destroying it.
- **Convergence is a three-way comparison** (authority digest / `synced_digest` /
  disk digest) over the normative table in KSP §5.1. Stage 1 runs the clean
  rows; stage 2 enables the push rows. Deletion is absence from the manifest —
  no tombstones, because a node distinguishes "deleted upstream" from "never had
  it" using its own record.
- **Writes are CAS** (`If-Match`). A conflict is an explicit `412`, never a
  silent merge or an arbitrary tiebreak. A watcher (no human in the loop)
  preserves its content to a conflict area **outside** the synced tree, then
  converges.

No sync loop is possible: applying a pulled file sets `synced_digest` to the
pulled digest, so the resulting watcher event classifies as "in sync".

## Why pull, not event-push

The obvious design fans a change event out from the leader. It does not fit this
codebase:

- **The event bus fans *in*, not out.** `forwardEvents` (`server.go:693`) is
  slave→master; `PublishClusterEvent` (`server.go:1221`) republishes onto *this
  node's own* bus. One `POST /api/v1/cluster/events` ingest exists and **no
  outbound push** — leader→node push would be net-new cluster infrastructure.
- **The bus is lossy by design.** `Publish` drops on a full subscriber channel
  (`eventbus.go:54`); `forwardEvents` is explicitly best-effort. Safe for agent
  lifecycle events only *because heartbeat digests re-derive state* — KSP would
  inherit the lossiness without the repair.
- **`server.Event` is closed** (`{Type, Node, AgentID, Name}`) and its doc
  comment is load-bearing: *"Events carry no sensitive payload … so they are
  safe to propagate across nodes."* KB paths are project content metadata.

Polling keeps every call node→leader (the direction the codebase supports),
makes missed updates impossible by construction, and leaves `Event` untouched. A
change signal to trigger an early poll is a later latency optimization only.

## The scope seam

One interface carries everything kind-specific, so registering a second kind
later touches nothing else:

```go
// scopeResolver binds a knowledgebase scope kind to the host. Everything else
// in the converger is kind-independent.
type scopeResolver interface {
    Kind() string
    Validate(id string) error                       // is this a real scope?
    IsAuthority(id string) bool                     // do we hold canonical state?
    AuthorityTree(id string) (string, error)        // canonical path, when authority
    LocalTree(id string) (string, error)            // node-local materialization path
    Participates(id string) bool                    // do we sync this at all?
    Authorize(r *http.Request, id string, w bool) error
}
```

Convergence, the manifest, the three-way table, and the CAS handlers take a
`{kind, id}` and a resolver; only `projectScope` implements it. This is a cheap
shape to adopt now (it is roughly the indirection the project case needs anyway,
since authority-vs-participant path resolution already differs) and expensive to
retrofit, because it otherwise diffuses into route shapes, on-disk record keys,
and the authorization call sites.

Two things must be scope-keyed on disk from the start, for the same reason:
sync records (KSP §2.4) and the local tree root. A flat per-project layout would
have to be migrated later.

## Preconditions this exposes

- **Distinct workspaces required.** `defaultProjectWorkspaceDir = "."`
  (`horde.go:276`) means two projects created without an explicit workspace
  share one `./.horde/knowledgebase/`. Enabling sync for a project with a shared
  or unset workspace MUST fail validation with a clear error.
- **Participants use a node-local *workspace*, not a cache.** The project's
  `Workspace` is an authority-side path that may not exist elsewhere.
  Participants materialize the KB under a node-local workspace root
  (`<data_dir>/workspaces/<kind>/<id>/` by default — scope-keyed from the start,
  so a second kind needs no migration) — **a place users and
  agents actually work**, because in stage 2 they edit there. Same location in
  both stages, so promotion needs no migration.
- **Participants learn projects from the authority.** There is no node↔project
  association (`Team` is `{Agents, Users}`, `project.go:39`; `knownSlave` is
  `{addr, agents, lastSeen}`, `server.go:1234`) and a slave's local
  `ProjectStore` is permanently empty (`projectForwardMiddleware` forwards every
  `/projects` request). No new primitive is needed: a participant lists projects
  from the leader over the existing forward path and syncs those with sync
  enabled.

# Slices (each independently shippable + backward-compatible)

## Stage 1 — shared, readable, writable by API

1. **Authority manifest + read API.** ✅ The `scopeResolver` seam with its single
   `projectScope` implementation; digest-based manifest over a scope's canonical
   tree; `GET /api/v1/kb/{kind}/{id}/manifest` (with `If-None-Match`/`304`) and
   `GET …/file`; authorization per KSP §9. Scan on request, cache by mtime.
   Unregistered kinds `404`. Ships value alone: the KB becomes readable over the
   API and the TUI.
2. **Authority watcher.** ✅ fsnotify on the canonical tree, debounced, maintaining
   the manifest incrementally. Editing a file on the authority now shows up
   through the API. *This watcher component is reused verbatim in slice 5.*
3. **Participant convergence.** ✅ Node-local workspace root; persisted
   scope-keyed sync records (`synced_digest`); periodic manifest poll →
   three-way classify (KSP §5.1) → pull / delete-locally; dirty local files
   preserved to the conflict area rather than overwritten (KSP §5.2). Verify a
   fetched manifest's scope matches the one requested. Serves local reads labeled
   `X-KSP-Authority: participant`. **The KB is now shared across hosts.**
4. **CAS writes.** ✅ `PUT`/`DELETE /api/v1/kb/{kind}/{id}/file` with mandatory
   `If-Match`/`If-None-Match: *`, per-path serialization on the authority,
   temp+rename, digest verification; participant-node writes forward to the
   authority and return `412` verbatim. Every node is a write entry point.

## Stage 2 — symmetric multi-writer

5. **Participant watcher + push.** ✅ Point the slice-2 watcher at the
   participant's tree and enable the push rows of KSP §5.1, using
   `synced_digest` as the `If-Match` base. The watcher triggers an early
   convergence pass via a buffered channel; the converger classifies local
   edits even on a 304 (manifest unchanged) because local edits need to be
   pushed. A 412 conflict preserves the local content to the conflict area
   before converging to canonical. **Local file edits on any node now
   propagate** — the goal.
6. **Offline durability + conflict handling.** ✅ Offline edits stay on
   disk with `D ≠ S`; the three-way comparison + persisted sync records are
   the replay mechanism — on reconnect the next poll classifies and pushes.
   The conflict area (`<data_dir>/kb-conflicts/<kind>/<id>/`, uniquely named
   per KSP §6.1) with operator surfacing via `GET /api/v1/kb/{kind}/{id}/conflicts`.

## 7. Docs/KB

Finalize KSP v1; new decision doc; environment docs; a pattern doc; roadmap.
Per-slice `log.md` entries land throughout, not only here.

## Known implementation friction

- **`forwardRequest` cannot carry the CAS headers.** It hardcodes
  `Content-Type: application/json` and sets only `X-Horde-User`
  (`leaderclient.go:163-176`); its signature has no header parameter, and
  `ForwardProjectRequest` (`server.go:1143`) and the `projectForwarder`
  interface (`internal/api/types.go:112`) mirror it. Slice 4 must extend those
  (3 call sites + the fake at `internal/api/project_forward_test.go:27`) or add
  a dedicated KB client. A markdown body under `application/json` is also wrong.
- **KB routes must not sit inside the forwarded `/projects` group.**
  `projectForwardMiddleware` forwards unconditionally when a leader is set
  (`project_forward.go:19-22`), so a participant could never serve its local
  copy. The scope-keyed route root (`/api/v1/kb/{kind}/{id}/…`) sits outside that
  group by construction, which is a second reason to prefer it over
  `/api/v1/projects/{id}/kb/…` — it makes the requirement structural instead of
  an exception. Convergence uses its own leader client; slice 4 forwards writes
  explicitly.
- **Authorization needs both paths.** For `project` scope, reads resolve through
  `authorizeProject(…, levelView)` (`authz.go:41`) — behind
  `scopeResolver.Authorize`, so a later kind substitutes its own rule — and must
  *not* inherit the open-reads policy (`router.go:74-79`), since redaction cannot
  redact document bytes. Separately, `authorizeProject`'s node branch requires an echoed
  `X-Horde-User` and returns 403 without one (`authz.go:53-67`), but convergence
  pulls are machine-initiated with no user — a node-principal convergence-read
  path is required or sync fails closed once auth is enabled (KSP §9).
- **Body buffering.** `projectForwardMiddleware` and `forwardRequest` both
  `io.ReadAll` whole bodies and `leaderClientTimeout` is 5s
  (`leaderclient.go:31`). Keep `max_file_size` small (KB docs are kilobytes —
  default 1 MiB) or give the KB client its own timeout.
- **Watcher lifecycle.** There is no `Stop()` — teardown is ctx-driven
  (`server.go:497-502`). Follow `startHealthPolling(ctx)` (`server.go:490`); the
  `*fsnotify.Watcher` holds real fds and must close on ctx cancel or
  `-race`/goroutine-leak checks fail. Specify add/remove on project
  create/finish/delete.
- **Empty `StateDir`/`DataDir`.** Existing consumers guard on emptiness
  (`server.go:408, 423`). Sync records, workspace root, and conflict area must
  define in-memory-only / disabled behavior rather than writing to `/`.

# Config (`internal/config/horde.go`)

Section spelled out to match the family (`ServerConfig`, `ClusterConfig`,
`AgentConfig`, `ProjectConfig`, `AuthConfig`):

```go
type KnowledgebaseConfig struct { Sync KBSyncConfig `mapstructure:"sync"` }
type KBSyncConfig struct {
    Enabled       bool     `mapstructure:"enabled"`        // opt-in; default false
    WatchLocal    bool     `mapstructure:"watch_local"`    // stage 2; default false
    WorkspaceRoot string   `mapstructure:"workspace_root"` // participant KB root
    PollInterval  string   `mapstructure:"poll_interval"`  // default "30s"
    DebounceMS    int      `mapstructure:"debounce_ms"`    // default 500
    MaxFileSize   int64    `mapstructure:"max_file_size"`  // default 1 MiB
    Ignore        []string `mapstructure:"ignore"`         // globs, KB-root-relative
}
```

On `Config`: `Knowledgebase KnowledgebaseConfig` with
`mapstructure:"knowledgebase"` (env `HORDE_KNOWLEDGEBASE_SYNC_ENABLED`, …).
`watch_local` is the stage-2 switch, so stage 2 ships opt-in on top of a stable
stage 1. Add `Config.KBSyncEnabled()` and a `validateKnowledgebase()` method
(separate, for gocyclo, like `validateAuth`/`validateCluster`).

These keys are the **cross-kind defaults** (poll, debounce, size, ignore,
workspace root). A later scope kind adds its own sub-block for what is genuinely
kind-specific — enablement and, where relevant, participation — rather than
reshaping this one. Deliberately *not* done now: a `scopes:` map with one entry,
which buys nothing while `project` is the only kind.

**Every key must be added to the `defaults` map** (`horde.go:283`): viper's
`AutomaticEnv` only resolves keys it already knows, so a key absent there
silently ignores its `HORDE_*` override. Thread into `server.Config` via
`cmd/serve.go`, and ensure the zero-value `server.Config` used by existing unit
tests leaves sync **off** (the `SpawnDefaultAgent: false` discipline analogue —
no unit test may start a watcher).

# Tests (respect the `//go:build integration` split)

- **Unit** (pure, deterministic — no watcher, no timers): **the KSP §5.1
  three-way table as a table-driven test over `(A, S, D)` → action** (this is the
  correctness core and it is a pure function — it must be exhaustively covered);
  `manifest_digest` stability and ordering; path-traversal + size rejection; CAS
  precondition evaluation (match / mismatch / create-exists / missing-header
  428); conflict-area naming uniqueness; debounce coalescing as a pure function
  over synthetic events with an injected clock; `validateKnowledgebase` table;
  **disabled-by-default is a no-op** (config-level — belongs in the unit suite so
  CI actually runs it, not behind the integration tag); **scope handling** — an
  unregistered kind returns `404`, a manifest whose scope differs from the one
  requested is rejected, and sync-record keys for two scopes do not collide.
- **Integration** (`//go:build integration` — real fds, timing, multi-node): a
  real fsnotify watcher observing an edit (including editor write-temp-rename);
  two-node convergence (edit on authority → appears on participant within a
  poll); delete-by-absence; participant catch-up from empty; a participant that
  missed N updates converging in one poll; CAS conflict returning 412 through
  the forward path; auth-enabled convergence (node-principal pull without
  `X-Horde-User` succeeds; anonymous user read denied); **stage 2**: edit on a
  participant propagating to the authority and on to a third node; a concurrent
  edit on two nodes producing exactly one canonical winner plus a preserved
  conflict copy on the loser; an offline edit replaying on reconnect.

# Future scopes

Not built here. Each is a separate piece of work, and none is blocked on KSP —
they are blocked on modelling or policy that does not exist yet. Registering one
means supplying the four bindings in KSP §12.1 (identity, authority, location,
authorization) plus, for the narrow ones, participation.

- **`cluster` — organizational knowledge. The easiest.** Identity is a constant,
  authority is the leader, authorization is read-all / write-admin (the
  `UserConfig.Admin` flag at `horde.go:215` already exists). It needs only a
  defined location outside any project workspace. Plausibly the next scope after
  project, and cheap.
- **`team` — tribal knowledge. Blocked on the data model.** `Team` is not an
  entity: it is a struct *inside* a project (`Project.Team`, `project.go:38-42`,
  `{Agents []TeamAgent, Users []TeamUser}`) with no id, no store, and no
  existence outside its project. A team knowledgebase cannot be *named* by a
  scope, let alone located or authorized. First-class cross-project teams with
  stable ids and membership are the prerequisite — a change to the
  [project/team/user model](../decisions/project-team-user-model.md), not to
  sync.
- **`user` — individual knowledge. Blocked on identity and participation.** Two
  problems. (1) There is deliberately **no replicated user store**
  ([per-user-token-auth](../decisions/per-user-token-auth.md)): users are
  per-node config entries and cross-node identity is the `X-Horde-User`
  echo-trust seam, so user ids are cluster-consistent only by config convention.
  (2) A participant materializes a whole tree on local disk, and the
  convergence-read path lets any node holding the cluster token read it (KSP §9)
  — so fanning a personal knowledgebase to every node is a disclosure problem.
  Needs **selective participation** (KSP §3.2): sync a user's scope only to nodes
  where that user is active. That is real new policy, not a binding.

## Open question: composition across scopes

Once more than one scope exists, an agent working in project P, for user U, on
team T should plausibly see **one** view rather than four trees to search. That
needs a resolution model — precedence order (cluster → team → project → user, or
some other), whether a narrower scope shadows or merges with a wider one at the
same path, and whether composition happens at read time or is materialized.

**Unresolved, and deliberately not designed here.** It is a *consumer* concern,
not a replication one: KSP replicates each scope independently and takes no
position (KSP §1.1). It is recorded because it is the thing that makes several
knowledgebases useful instead of several places to look — and because it may
constrain the on-disk layout, so it should be settled before the second scope
lands, not after.

# Risks / edge cases

- **Simultaneous edits to one file cannot be auto-merged.** Intrinsic to
  file-granular sync, not to this design: the loser gets a preserved copy in the
  conflict area and must reconcile by hand. Only structured/CRDT merge avoids
  it, and that is a protocol revision. For OKF docs (people mostly edit
  different files) this is an acceptable trade — but it is the one limitation
  that does not go away at stage 2.
- **Canonical tree survival across failover.** KSP §7 makes authority change
  safe for *convergence*, but a newly-elected authority serves its own tree,
  which must itself be current. Until the tree is replicated (not just
  metadata), do not enable sync together with automatic raft failover.
- **Stage 1 dirty local files.** Preserved to the conflict area, not silently
  destroyed (KSP §5.2) — but they still do not propagate. Consider making the
  participant tree read-only on disk in stage 1 so the limitation is immediate
  rather than discovered later.
- **Poll latency.** Steady-state propagation is bounded by `poll_interval`
  (default 30s). `If-None-Match`/`304` keeps the cost near zero; a change signal
  is the later optimization.
- **Editor write patterns.** Debounce and treat write-temp-then-rename as one
  change; default ignore globs must cover editor swap files.
- **Git coexistence.** Never touch `.git/`; ignore git's churn. Sync is live
  distribution, git is durable history.
- **Disclosure.** Every participating node stores project documents on disk;
  treat those trees and the conflict area as project-confidential.

# KB / docs to update on landing

- **Finalize** [KSP v1](/docs/spec/knowledgebase-sync-protocol-v1.md).
- **New decision** `decisions/knowledgebase-sync.md`: authority-serialized
  multi-writer, why not event-push (fan-in / lossy / closed-`Event`), why
  digest-identity rather than versions, why `synced_digest` is the pivot, why the
  protocol is scope-parameterized while only `project` ships, and the staging
  rationale.
- **Update** `decisions/persistence-and-knowledgebase.md` §4 (fill the deferred
  sync half), the [roadmap](roadmap.md), `docs/environment.md` +
  `concepts/environment.md` (`knowledgebase.sync.*`), `patterns/index.md` (a
  three-way-convergence pattern); `log.md` per slice.

# Verification (when implemented)

- `task test` (unit, `-race`) + `task lint` + `task fmt`, then
  `task test:integration` for the multi-node convergence suites.
- Manual (dev node under overmind+air — use the overmind MCP tools; do not
  hand-start/kill horde) plus a second node on a distinct port: enable sync;
  edit a doc on the authority, confirm it appears on the participant within a
  poll; delete it, confirm removal; write from the participant via the API,
  confirm CAS behavior; stop the participant, make several edits, restart and
  confirm one-poll convergence; **stage 2**: enable `watch_local`, edit a file
  directly on the participant and confirm it propagates back; edit the same file
  on both nodes at once and confirm one winner plus a preserved conflict copy;
  disable sync and confirm fully-local behavior returns.

# See also

* [Data persistence and per-project knowledgebase](../decisions/persistence-and-knowledgebase.md)
  — §4 named this the shared brain and deferred sync; this builds it.
* [Knowledgebase Sync Protocol v1](/docs/spec/knowledgebase-sync-protocol-v1.md)
  — the wire format (complete for both stages).
* [Cluster leader failover](../concepts/cluster-failover.md) — the elected
  leader that becomes the authority; see the canonical-tree-survival risk.
* [Cluster mTLS](../concepts/cluster-mtls.md) — later transport hardening for
  sync traffic (not a prerequisite).
