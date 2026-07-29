# Knowledgebase Sync Protocol (KSP) — v1

Status: **Draft**
Protocol version: **1**
Canonical home: **horde** (`github.com/geoffjay/horde`)
Implementation plan: [Phase 6 — Knowledgebase sync](/docs/knowledgebase/plans/phase-6-knowledgebase-sync.md)

## 1. Purpose and scope

The Knowledgebase Sync Protocol (KSP) defines how horde nodes keep each
project's per-project OKF knowledgebase (`.horde/knowledgebase/`) synchronized
across every node registered to that project. It specifies the file model, the
manifest, the wire messages, the authority model, and conflict resolution — the
"live shared brain" that lets distributed people and AI agents work against one
knowledgebase.

KSP governs **file-content replication of a bounded document subtree** between
nodes of one cluster. It deliberately does **not** govern:

- **General workspace sync.** Only `.horde/knowledgebase/` syncs; the rest of a
  project's workspace is out of scope (that would be a general file-sync
  product).
- **Transport auth.** KSP rides the host's existing node→node auth (the shared
  `cluster.auth_token`, later mTLS). KSP carries a *user attribution* header but
  does not itself authenticate.
- **Structured record merge.** KSP is **file-based**: the unit is a whole file.
  Structured/CRDT frontmatter merge is a possible future version; v1 is
  last-writer-wins per file.
- **Durable history.** Git (if the workspace is a repo) remains the durable,
  human-authored history. KSP is the live propagation channel; the two coexist.

## 2. Model

### 2.1 File

A **file** is one regular file whose path is relative to a project's
`.horde/knowledgebase/` root (the "KB root"). Paths use `/` separators, are
normalized, and MUST NOT escape the KB root: any path containing a `..`
component, an absolute path, or a symlink that resolves outside the root MUST be
rejected. A conforming node MUST reject a file larger than the configured
maximum (`kb.sync.max_file_size`, default 10 MiB) and MAY skip paths matching a
configured ignore glob (`kb.sync.ignore`).

### 2.2 File record

Every file the leader knows about has a **record**:

| Field | Type | Meaning |
| --- | --- | --- |
| `path` | string | Path relative to the KB root. |
| `digest` | string | `sha256:<hex>` of the file bytes (of the tombstone marker for a delete). |
| `version` | uint64 | Monotonic per-path version assigned by the **leader** on accept. |
| `timestamp` | RFC 3339 | The origin's file/frontmatter modification time (LWW tiebreak only). |
| `origin` | string | Node id that authored this version (attribution + final tiebreak). |
| `deleted` | bool | True for a tombstone (see §5.3). |

`version` is authoritative for ordering. `timestamp` and `origin` are tiebreaks
only (§5), so node clock skew cannot reorder canonical history.

### 2.3 Manifest

A **manifest** is the set of records for one project's KB: `path → record`. The
leader holds the canonical manifest. Each node holds a local manifest reflecting
what it has applied. Reconciliation (§6) is a manifest diff.

## 3. Roles

- **Leader** — the project's authority (horde's master, or the Phase 5
  raft-elected leader). Holds the canonical KB tree and manifest, assigns
  `version`, and fans changes out.
- **Node** — any node registered to the project (including the leader). Watches
  its local KB root, pushes local changes to the leader, and applies changes the
  leader announces.

A node that is itself the leader short-circuits the push/pull over loopback (it
writes canonically and fans out directly).

## 4. Messages

KSP v1 binds to horde's HTTP+SSE transport. All routes are project-scoped and
authenticated by the host's node/user auth. The user-attribution header
`X-Horde-User` is echoed on node→node calls and honored only for a node
principal (see the host's echo-trust seam).

### 4.1 `GET /api/v1/projects/{id}/kb/manifest`

Returns the responder's manifest as JSON: `{ "files": [ <record>, … ] }`.
Used for reconciliation diffs and read-only inspection.

### 4.2 `GET /api/v1/projects/{id}/kb/file?path=<path>`

Returns one file's bytes (200) with `X-KSP-Version`, `X-KSP-Digest`,
`X-KSP-Timestamp`, `X-KSP-Origin` response headers; 404 if unknown; 410 if the
path is a tombstone.

### 4.3 `PUT /api/v1/projects/{id}/kb/file?path=<path>`

Push a changed file to the leader. Body = file bytes. Request headers carry
`X-KSP-Digest`, `X-KSP-Timestamp`, `X-KSP-Origin`, and `X-KSP-Base-Version` (the
version the origin last saw for this path, or `0` if new). The leader applies
LWW (§5) and responds:

- `200` + the accepted `X-KSP-Version` when the push wins (or is a no-op
  because the digest already matches).
- `409` + the current record when the push loses to a newer canonical version;
  the origin MUST reconcile (pull the canonical file, re-resolve locally).

### 4.4 `DELETE /api/v1/projects/{id}/kb/file?path=<path>`

Propagate a delete. The leader records a tombstone (§5.3) with a new `version`,
same LWW rules and responses as `PUT`.

### 4.5 `kb.file.changed` event

When the leader accepts a change (PUT or DELETE), it emits a `kb.file.changed`
event on the host event bus and republishes it cluster-wide (reusing the host's
`POST /api/v1/cluster/events` fan-out). Payload: `{ project_id, path, version,
digest, deleted, origin }` — **notification only, no bytes**. A node receiving
an event for a `version` newer than its local record pulls the file (§4.2) and
applies it (§5, §7).

## 5. Conflict resolution (last-writer-wins)

For a given `path`, when the leader must choose between an incoming change and
its current canonical record, the **winner** is determined in order:

1. **Higher `version`** wins. (An incoming push carries `X-KSP-Base-Version`; if
   it is less than the canonical `version`, the push is stale → `409`.)
2. If versions are equal (concurrent same-base edits), **higher `timestamp`**
   wins.
3. If timestamps are equal, **higher `origin`** (lexicographic node id) wins —
   an arbitrary but *deterministic* tiebreak so all nodes converge identically.

The winner is assigned `version = canonical.version + 1`. The loser's content is
preserved for the user as a `<path>.conflict-<origin>` sidecar on the losing
node and surfaced via a `kb.file.conflict` event; KSP v1 does not auto-merge.

## 6. Reconciliation (join / rejoin)

On registering to a project (or reconnecting), a node performs a **manifest
diff** against the leader (§4.1):

- **Leader has newer** (`leader.version > local.version`, or local absent): pull
  and apply (§7).
- **Local has newer** (`local.version > leader.version`, or leader absent and no
  tombstone): push (§4.3). The leader applies LWW.
- **Equal version, equal digest**: no-op.
- **Equal version, differing digest**: a concurrent edit made while
  disconnected → resolve by LWW (§5) via a push; the leader's `409`/`200` drives
  the outcome.

Reconciliation is idempotent and safe to repeat. A node's unpushed local edits
made while disconnected are preserved on disk and surface as "local newer"
during the next reconciliation.

## 7. Applying a remote change (loop suppression)

**Correctness-critical.** When a node applies a pulled file (or tombstone), it
MUST NOT let its own watcher interpret that write as a new local change to push
back. A conforming implementation MUST either:

- apply the write through a path that suppresses the watcher emit for that
  change, **and/or**
- short-circuit any outbound change whose `digest` already equals the local
  record's `digest` for that `path`.

Both together are RECOMMENDED. Without this, two nodes ping-pong a file forever.

## 7.1 Tombstones (deletes)

A delete is recorded as a **tombstone**: a record with `deleted: true` and a new
`version`. Tombstones propagate like content changes (event → the node removes
its local file and records the tombstone). Tombstones let a node distinguish
"deleted" from "never had it" during reconciliation, preventing a deleted file
from resurrecting on rejoin. A node MAY garbage-collect tombstones older than a
retention window once all known members have acknowledged the version; v1 keeps
them indefinitely for simplicity.

## 8. Opt-in and compatibility

KSP is **opt-in**. With `kb.sync.enabled` false (the default), no watcher runs,
no KSP routes mutate, and the KB is purely local — byte-for-byte the pre-KSP
behavior (git-backed if the workspace is a repo). This is a hard requirement:
the disabled state is a true no-op.

KSP v1 evolves **additively**: new optional record fields, headers, or event
payload keys may be added without a version bump; a receiver MUST ignore unknown
fields. Breaking changes bump the protocol version.

## 9. Security considerations

- **Transport.** KSP relies on the host's node auth. An external client cannot
  push to the leader without the cluster token; `X-Horde-User` is honored for
  attribution only when the caller is a node principal (host echo-trust rule).
- **Path traversal.** §2.1 rejection of `..`/absolute/escaping-symlink paths is
  mandatory — a push must never write outside the project's KB root.
- **Size / resource.** The size cap and ignore globs bound what a compromised or
  buggy peer can push; the leader enforces both on accept.
- **Content trust.** KSP moves document bytes; it does not execute them. Agents
  that *act on* KB content do so under the host's separate tool-approval and
  (future) filesystem-sandboxing layers.

## 10. Open questions (v1 → later)

- Structured/CRDT merge of OKF frontmatter instead of whole-file LWW.
- Delta/patch transfer for large docs instead of whole-file pulls.
- Manifest replication through the raft log for instant post-failover authority.
- Peer-to-peer (non-leader) propagation for partition tolerance.
