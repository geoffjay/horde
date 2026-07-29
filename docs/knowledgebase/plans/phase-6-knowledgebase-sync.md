---
type: Plan
title: Phase 6 — Knowledgebase sync (the distributed shared brain)
description: Proposed plan for synchronizing each project's per-project OKF knowledgebase across every node registered to the project — a file watcher, leader-authoritative file-content replication over HTTP, cross-node read APIs, join/leave reconciliation, and last-writer-wins conflict resolution. Opt-in and backward-compatible; the sync wire format is specified separately.
tags: [plan, knowledgebase, sync, distributed, cluster, projects, phase-6]
timestamp: 2026-07-28T00:00:00Z
---

> **Status: planned.** No slice has landed. This phase builds horde's
> central differentiator — the per-project knowledgebase as a *live,
> cluster-synchronized* shared brain — which the
> [persistence-and-knowledgebase decision](../decisions/persistence-and-knowledgebase.md)
> §4 named as "the hardest problem" and explicitly deferred ("Slice B creates
> the KB but does not sync it"). The sync **wire format** is specified in
> [Knowledgebase Sync Protocol v1](/docs/spec/knowledgebase-sync-protocol-v1.md);
> this plan is the horde-side implementation.

# Context

Every project already gets a local `.horde/knowledgebase/` OKF tree, scaffolded
at creation by `scaffoldKnowledgebase` (`internal/server/knowledgebase.go`),
non-destructively (`writeIfNotExists`). But that tree is **local only**: today
it "travels with the workspace (e.g. checked into git)" — the code says so at
`knowledgebase.go:87`. Nothing watches it, nothing replicates its *files*, and
no API reads them across nodes.

What the cluster replicates today is **metadata only**: the raft FSM carries
project/team metadata and AAP resume tokens (`internal/server/raftstate.go`,
`raftproject.go`); the `Project.Workspace` field is a **path string**, never
file content. The cross-node machinery that exists moves *events*
(`forwardEvents`, `server.go:687`), *project-API requests*
(`ForwardProjectRequest`, `server.go:1136`), and *invoke routing*
(`RemoteAgentNode`, `server.go:1338+`) — never file bytes.

So a synchronized, watched, cluster-replicated document brain is **net-new**.
This phase delivers it: a file-watch layer, a file-content replication path,
cross-node read APIs, and reconciliation — none of which exist even in stubbed
form.

# Goal

When a node is registered to a project and KB sync is enabled, its local
`.horde/knowledgebase/` stays synchronized with the other nodes on that
project: local edits propagate to the cluster, and remote edits are applied
locally — so distributed people and agents work against one shared, live OKF
knowledgebase.

# Decisions

## Locked (recommended; open to change during planning)

1. **Leader-authoritative, file-based, last-writer-wins.** The project's leader
   (master, or the Phase 5 raft-elected leader) is the KB authority for that
   project. Every registered node watches its local KB tree and pushes changes
   to the leader; the leader holds the canonical tree, assigns a per-path
   version, and fans a change notification out to the other project nodes,
   which pull. This mirrors the existing `ForwardProjectRequest`
   forward-to-leader pattern and avoids N-way peer conflict resolution in the
   first slices. Peer-to-peer gossip of file changes is a possible future
   evolution, not the starting point.
2. **File content travels over HTTP, never the raft log.** The raft log is for
   small deterministic commands + snapshots; file blobs would bloat both. The
   sync bytes ride the existing HTTP+SSE transport (new project-scoped KB
   endpoints). A small **manifest** (path → digest + version + timestamp) *may*
   later replicate through raft so a newly-elected leader knows canonical
   versions (a failover-correctness slice), but that is metadata, not content.
3. **Opt-in and backward-compatible.** A new `kb.sync` config block; disabled by
   default. No `kb.sync.enabled` ⇒ the KB stays local + git-backed, byte-for-byte
   current behavior (mirrors the [per-user-auth](../decisions/per-user-token-auth.md)
   opt-in discipline). The disabled-by-default regression test is the guard.
4. **Scope strictly to `.horde/knowledgebase/`.** Only files under a project's
   KB subtree sync. The rest of the workspace does not (that would be a
   general-purpose file-sync product, out of scope). Path-traversal is rejected;
   file size is capped.
5. **LWW by server-assigned version, timestamp as tiebreak.** The leader assigns
   a monotonically increasing version per path on accept; higher version wins;
   equal version → higher OKF frontmatter/file `timestamp`; equal timestamp →
   deterministic node-id tiebreak. Server-assigned version is primary precisely
   so clock skew across nodes cannot desynchronize the canonical order. Richer
   structured/CRDT frontmatter merge is deferred.

## Baked in during planning (open to change)

- **Security rides the cluster auth.** Node→node KB traffic uses the existing
  shared `cluster.auth_token`; `X-Horde-User` is echoed for *attribution* of who
  made a change (who wrote a doc), reusing the
  [echo-trust seam](../patterns/principal-middleware-and-echo-trust.md). mTLS is
  the later transport hardening (see [cluster mTLS](../concepts/cluster-mtls.md)),
  not a prerequisite.
- **Sync and git coexist.** The workspace may be a git repo; sync operates on
  files and does not fight git. Sync is the *live* propagation channel; git
  remains the durable, human-authored history. Documented, not enforced.
- **Loop suppression is mandatory.** Applying a pulled file must not re-trigger
  the watcher into re-pushing it. Apply-without-emit + digest-equality check
  before emitting a change. This is the correctness lynchpin (see Risks).

# Architecture

```
node A (slave, registered to project P)         leader (authority for P)
  .horde/knowledgebase/  ──watch──▶ change        canonical .horde/kb/
        ▲                          │ PUT file ───────▶ validate path + size
        │ apply (no emit)          │                   assign version
        │                          ▼                   store canonical
        └── pull file ◀── kb.file.changed ◀────────── fan out cluster event
                          (event bus)                  (forwardEvents pattern)
```

- **Watcher** (`internal/server/kbwatch.go`, new): one fsnotify watcher per
  active project KB tree, debounced, emitting `(path, op, digest, mtime)` change
  records. `fsnotify` is already an indirect dep (`go.mod`); promote to direct.
- **Manifest** (`internal/server/kbmanifest.go`, new): path → `{digest,
  version, timestamp, origin}` for a project's KB. In-memory + a JSON flush to
  the state dir (per the persistence decision's state-dir category).
- **HTTP surface** (new, project-scoped, gated by `requireUser`/cluster auth):
  - `GET  /api/v1/projects/{id}/kb/manifest` — list path → digest/version.
  - `GET  /api/v1/projects/{id}/kb/file?path=…` — read one canonical file.
  - `PUT  /api/v1/projects/{id}/kb/file?path=…` — push a changed file (body =
    content; headers carry digest/version/timestamp/origin).
  - `DELETE /api/v1/projects/{id}/kb/file?path=…` — propagate a delete.
- **Fan-out**: a new `kb.file.changed` event on the existing `EventBus`, pushed
  slave→leader and republished leader→cluster exactly like agent lifecycle
  events (`server.go:687-714`, `POST /api/v1/cluster/events`).
- **Read path**: `GET .../kb/file` on any node serves its local (or, for a
  non-authority node, the leader's) canonical copy — the "read-only cross-node
  query" the collaboration model calls for.

# Slices (each independently shippable + backward-compatible)

1. **Local watch + read API.** fsnotify watcher on active projects'
   `.horde/knowledgebase/`, debounced, emitting `kb.file.changed` on the local
   `EventBus`; `GET .../kb/manifest` + `GET .../kb/file`. No cross-node write
   yet. Ships value alone: a node (or the TUI) can *read* a project's KB over
   the API, and cross-node reads work via the existing request-forward path.
2. **Push to leader + canonical store.** A registered slave's watcher `PUT`s
   changed files to the leader; the leader validates path + size, assigns a
   version, writes the canonical file, and records the manifest. Reuses the
   forward-to-leader pattern. Leader becomes source of truth.
3. **Fan-out + pull.** The leader republishes `kb.file.changed` to the cluster;
   registered nodes subscribe and pull the changed file. Edits now propagate
   end-to-end **while connected**. Loop-suppression (apply-without-emit) lands
   here — the correctness gate.
4. **Join / rejoin reconciliation.** On register/reconnect, a node diffs its
   manifest against the leader's and does bidirectional catch-up: pull
   missing/stale, push local-newer (LWW). Covers nodes joining and offline
   edits made while disconnected.
5. **Conflicts, deletes, renames, hardening.** LWW version/timestamp tiebreak;
   delete propagation via tombstones (so "missing on join" ≠ "deleted"); rename
   as delete+create; conflict surfacing (a `.conflict` sidecar + a surfaced
   event); path-traversal + size + ignore-glob guards hardened.
6. **Docs/KB.** Finalize the [sync protocol spec](/docs/spec/knowledgebase-sync-protocol-v1.md);
   new decision doc; `docs/environment.md` + `concepts/environment.md`
   (`kb.sync.*`); a sync pattern doc; roadmap → complete; `log.md` per slice.

*(Optional later, tied to Phase 5)* **Failover correctness.** Replicate the KB
manifest (not bytes) through the raft log so a newly-elected leader knows
canonical versions and can serve pulls immediately after a failover.

# Config (`internal/config/horde.go`)

Add a `KB KBConfig` section with a `sync` sub-block:

```go
type KBConfig struct { Sync KBSyncConfig `mapstructure:"sync"` }
type KBSyncConfig struct {
    Enabled     bool     `mapstructure:"enabled"`      // opt-in; default false
    DebounceMS  int      `mapstructure:"debounce_ms"`  // watcher coalescing, default 500
    MaxFileSize int64    `mapstructure:"max_file_size"`// reject larger, default 10MiB
    Ignore      []string `mapstructure:"ignore"`       // globs, relative to the KB root
}
```

`Config.KBSyncEnabled() bool` + a `validateKBSync()` method (separate for
gocyclo, like `validateAuth`/`validateCluster`): disabled ⇒ nil; else validate
positive debounce/size and compilable globs. Thread into `server.Config` via
`cmd/serve.go`.

# Tests (respect the `//go:build integration` split)

- **Unit**: manifest diff (missing/stale/newer classification); LWW
  version→timestamp→node-id tiebreak; path-traversal + size rejection; watcher
  event → change-record mapping; debounce coalescing; loop-suppression
  (apply-without-emit does not produce an outbound change); `validateKBSync`
  table; disabled ⇒ no watcher started.
- **Integration** (`//go:build integration` — subprocess/timing/multi-node):
  two-node push→fan-out→pull propagation of an edit; join reconciliation
  catch-up (a node that missed edits catches up on register); LWW conflict
  (concurrent edits to one path converge to one canonical version on all
  nodes); delete propagation (tombstone, not resurrection on rejoin);
  **disabled-by-default regression** (no `kb.sync` ⇒ existing suite unchanged,
  KB stays local); node→node KB traffic authed by the cluster token only.

# Risks / edge cases

- **Sync loops** — applying a pulled file must not re-trigger a push. Suppress
  by applying without emitting + a digest-equality short-circuit before any
  outbound change. **Correctness-critical**; the loop-suppression unit test is
  the guard.
- **Editor write patterns** — many editors write-temp-then-rename; the watcher
  must debounce and treat atomic-rename-into-place as a single change, not
  churn on the temp file. Ignore-globs cover editor swap files.
- **Clock skew** — LWW uses the **server-assigned version** as primary, the
  timestamp only as a tiebreak, so skewed node clocks cannot reorder canonical
  history.
- **Delete vs. missing-on-join** — without tombstones, a node that deleted a
  file offline would have it resurrected on rejoin by the leader's copy.
  Deletes propagate as tombstones (slice 5).
- **Git coexistence** — sync writes files a git repo also tracks; sync must not
  touch `.git/` and should ignore git's own churn. Documented; sync is live
  propagation, git is durable history.
- **Large/binary files** — strict scope to `.horde/knowledgebase/` + a size cap
  keep this a *document* sync, not a general file-sync product.
- **Leader change mid-sync** (with Phase 5) — a new leader must know canonical
  versions; until the failover-correctness slice lands, a failover forces a
  full manifest reconciliation on next connect (correct, just less efficient).

# KB / docs to update on landing

- **Finalize** [Knowledgebase Sync Protocol v1](/docs/spec/knowledgebase-sync-protocol-v1.md)
  (currently Draft alongside this plan).
- **New decision** `decisions/knowledgebase-sync.md`: leader-authoritative vs
  peer choice, file-over-HTTP vs raft-log, LWW-by-version, opt-in parity, the
  loop-suppression rule, tombstone deletes, git coexistence.
- **Update** `decisions/persistence-and-knowledgebase.md` §4 (fill the deferred
  sync half), `plans/roadmap.md` (Phase 6 → in progress/complete),
  `docs/environment.md` + `concepts/environment.md` (`kb.sync.*`),
  `patterns/index.md` (a KB-sync pattern — watch → push-to-leader → fan-out →
  pull, with loop-suppression); `log.md` per slice.

# Verification (when implemented)

- `task test` (unit, `-race`) + `task lint` + `task fmt`, then
  `task test:integration` for the multi-node propagation/reconciliation suites.
- Manual (dev node under overmind+air — use the overmind MCP tools, do not
  hand-start/kill horde) plus a second node on a distinct port: enable
  `kb.sync`, register both to a project, edit a doc on one node, confirm it
  appears on the other; edit the same doc on both, confirm convergence; delete
  on one, confirm it does not resurrect on the other's rejoin; disable
  `kb.sync`, confirm fully-local behavior restored.

# See also

* [Data persistence and per-project knowledgebase](../decisions/persistence-and-knowledgebase.md)
  — §4 named this the shared brain and deferred sync; this phase builds it.
* [Knowledgebase Sync Protocol v1](/docs/spec/knowledgebase-sync-protocol-v1.md)
  — the wire format this plan implements.
* [Principal middleware + X-Horde-User echo-trust seam](../patterns/principal-middleware-and-echo-trust.md)
  — reused for change attribution across nodes.
* [Cluster mTLS](../concepts/cluster-mtls.md) — the later node→node transport
  hardening (not a prerequisite).
* [Leader failover](leader-failover.md) — the raft-elected leader that becomes
  the KB authority; the optional failover-correctness slice integrates here.
