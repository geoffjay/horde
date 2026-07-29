# Knowledgebase Sync Protocol (KSP) — v1

Status: **Draft**
Protocol version: **1**
Canonical home: **horde** (`github.com/geoffjay/horde`)
Implementation plan: [Knowledgebase sync](/docs/knowledgebase/plans/knowledgebase-sync.md)

## 1. Purpose and scope

The Knowledgebase Sync Protocol (KSP) defines how horde nodes share a project's
per-project OKF knowledgebase (`.horde/knowledgebase/`) across a cluster, so
distributed people and AI agents read and write one shared document set.

**The destination is symmetric multi-writer**: every participating node watches
its own knowledgebase tree, and a user or agent editing a file on *any* node has
that change propagate to all the others. KSP reaches it with one authority
serializing writes, content digests as identity, and compare-and-swap as the
write primitive — which together make convergence provable without CRDTs.

Node-side behavior is delivered in **two stages** (§11). The **wire format in
this document is complete for both** — a stage-2 node's file watcher calls
exactly the same `PUT`/`DELETE` with `If-Match` that a stage-1 API client calls.
What stages is which node initiates a write, never the protocol.

### 1.1 What KSP governs — and what it does not

KSP governs replication of a bounded document subtree between nodes of one
cluster. It does **not** govern:

- **General workspace sync.** Only `.horde/knowledgebase/` replicates.
- **Transport auth.** KSP rides the host's node auth (the shared cluster token,
  later mTLS). §9 specifies only *which principal may do what*.
- **Automatic merge of simultaneous edits to one file.** Whole files are the
  unit. A write that loses its compare-and-swap is surfaced, never merged (§6).
  Structured/CRDT merge is out of scope at v1 and would be a protocol revision.
- **Durable history.** Git remains the human-authored history; KSP is the live
  distribution channel.

## 2. Model

### 2.1 Path

A **path** is a `/`-separated path relative to a project's
`.horde/knowledgebase/` root. It MUST be normalized and MUST NOT escape the
root: any `..` component, absolute path, or symlink resolving outside the root
MUST be rejected with `400`. Implementations MUST reject files over the size cap
and MUST skip paths matching the ignore globs. **Size and ignore policy is the
authority's alone** — the only policy governing what enters canonical state —
and is published on the manifest (§2.4) so a node can detect a mismatched local
config rather than diverge silently.

### 2.2 Entry — identity is the content digest

Each file in the canonical tree has an **entry**:

| Field | Type | Meaning |
| --- | --- | --- |
| `path` | string | Path relative to the KB root. |
| `digest` | string | `sha256:<hex>` of the file bytes. **The identity of the content.** |
| `size` | int64 | Byte length. |
| `modified` | RFC 3339 | Authority-observed modification time. Informational. |
| `author` | string | User id that last wrote it, when known. Attribution. |

There is deliberately **no version counter**. Content is addressed by digest,
which is independent of which node is the authority — so an authority change
cannot reorder, regress, or corrupt sync state (§7). `modified` and `author` are
never consulted for ordering or conflict decisions.

### 2.3 Node state — `synced_digest` is separate from disk

Every node keeps, per path, a **sync record** that is distinct from both the
authority's entry and the bytes on its own disk:

| Field | Meaning |
| --- | --- |
| `synced_digest` | The digest this node last **agreed** with the authority — the last content it successfully pulled or pushed. |

`synced_digest` is the pivot of the whole protocol. Comparing it against the
on-disk digest is how a node distinguishes *"this file changed because I pulled
it"* from *"this file changed because a user edited it"* — the distinction that
makes multi-writer safe and whose absence causes silent lost updates. Nodes MUST
maintain it from stage 1, where it also lets a read-only node detect and preserve
an unexpected local edit (§5.2) rather than destroy it.

A node MUST persist sync records across restarts, and MUST treat a missing
record as "never synced" (not as "clean").

### 2.4 Manifest

A **manifest** is the complete set of entries for a project's canonical tree,
plus the policy a node needs:

```json
{
  "project_id": "p-123",
  "manifest_digest": "sha256:…",
  "authority": "node-a",
  "policy": { "max_file_size": 1048576, "ignore": ["*.tmp", ".git/**"] },
  "files": [ { "path": "index.md", "digest": "sha256:…", "size": 812,
               "modified": "2026-07-28T10:00:00Z", "author": "alice" } ]
}
```

`manifest_digest` is a digest over the sorted `(path, digest)` pairs — the cheap
change-detection primitive: equal digest ⇒ nothing to do.

The manifest is **complete**, not incremental. Completeness is what makes
convergence self-healing: a node that misses any number of updates converges on
its next poll, because the diff is computed against whole current state rather
than a stream of deltas. There is no event stream to miss, no gap to detect, and
no acknowledgement to track.

## 3. Roles

### 3.1 Authority

Exactly one node is the authority for a project: the cluster's leader (static
master, or the raft-elected leader). The authority holds the **canonical tree**,
watches it, serves reads (§4.1, §4.2), and is the sole applier of writes (§4.3,
§4.4) — every write in the cluster is serialized through it.

### 3.2 Participant — every other node

A participant keeps the project's knowledgebase in a **node-local workspace**:
`<local workspace root>/.horde/knowledgebase/`. This MUST be a location where
that node's users and agents actually work, **not** a hidden cache, and MUST NOT
be the authority's `Project.Workspace` path (a remote path that may not exist or
may mean something different locally). The location is identical in both stages,
so promoting a node from stage 1 to stage 2 requires no migration.

A participant converges by polling (§5). Whether it also *originates* changes
from local file edits is the stage distinction (§11):

- **Stage 1** — reads and converges; local edits are not a sync input, but are
  detected and preserved rather than destroyed (§5.2). Writes are possible via
  the API (§6), which forwards to the authority.
- **Stage 2** — watches its tree; a local edit is pushed to the authority via
  the same CAS write path.

## 4. Messages

KSP binds to horde's HTTP transport. Routes are project-scoped. Authorization is
§9. **This message set is complete for both stages.**

### 4.1 `GET /api/v1/projects/{id}/kb/manifest`

Returns the manifest (§2.4) of whichever node serves it. Header
`X-KSP-Authority: authority|participant` states whether it is canonical. A node
MUST resolve the **authority's** manifest for convergence, not its own.

Supports `If-None-Match: <manifest_digest>` → `304 Not Modified`, making the
steady-state poll nearly free.

### 4.2 `GET /api/v1/projects/{id}/kb/file?path=<path>`

Returns file bytes with `ETag: <digest>`, `X-KSP-Authority`, `Last-Modified`.
`404` if the path is not in the serving node's manifest.

### 4.3 `PUT /api/v1/projects/{id}/kb/file?path=<path>`

Write a file. Body = bytes. **Compare-and-swap is mandatory**:

- `If-Match: <digest>` — apply only if the current canonical digest matches.
- `If-None-Match: *` — apply only if the path does not exist (create).

Neither header ⇒ `428 Precondition Required`. Requiring the writer to state the
state it believes it is modifying is what makes "who wins" unambiguous with no
tiebreak rule — and it is what lets a stage-2 watcher push safely, using its
`synced_digest` (§2.3) as the `If-Match` value.

Responses: `200` + `ETag` on success; `412 Precondition Failed` + the current
entry when the precondition fails (§6); `409` if the path exists and
`If-None-Match: *` was given; `413` over the size cap; `400` on an invalid path
or a body mismatching a supplied `Content-Digest`.

The authority MUST verify the body against `Content-Digest` when present, MUST
serialize concurrent writes **per path**, and MUST write via temp-file +
`rename` so a reader never observes a torn file.

Writing content identical to current content is a **no-op success** (`200`, same
`ETag`), so retry after a lost response is safe.

### 4.4 `DELETE /api/v1/projects/{id}/kb/file?path=<path>`

Delete a file. `If-Match: <digest>` REQUIRED; `412` on mismatch, `404` if absent.
No tombstone is recorded: deletion is absence from the next manifest, which is
unambiguous because a node distinguishes "deleted upstream" from "I never had
it" using its own `synced_digest` (§5.1), not cluster-wide history.

## 5. Convergence

### 5.1 The three-way comparison

A node converges by comparing three values per path: the **authority's** entry
digest (`A`), its own **`synced_digest`** (`S`), and its **on-disk** digest (`D`).
This table is normative and complete; stage 1 executes the clean rows and
handles the dirty rows per §5.2.

| `A` | `S` | `D` | Meaning | Action |
| --- | --- | --- | --- | --- |
| `x` | `x` | `x` | In sync | none |
| `y` | `x` | `x` | Remote change, clean local | pull → `S=y` |
| `x` | `x` | `z` | Local edit only | push `If-Match: x` → on `200`, `S=z` |
| `y` | `x` | `z` | Concurrent remote + local change | conflict (§6) |
| — | `x` | `x` | Deleted upstream, clean local | delete locally, drop record |
| — | `x` | `z` | Deleted upstream, edited locally | conflict (§6) |
| `x` | `x` | — | Deleted locally | push `DELETE If-Match: x` |
| `x` | — | `z` | Exists upstream, untracked local file | conflict (§6) |
| — | — | `z` | New local file | push `If-None-Match: *` |
| `y` | — | — | New upstream file | pull → `S=y` |

Convergence is idempotent and restart-safe: a node may crash or start empty and
re-derive everything from the manifest plus its persisted records.

Because a node never applies a change it did not first classify here, and never
pushes content equal to its `synced_digest`, **there is no sync loop** — applying
a pulled file sets `S` to the pulled digest, so the resulting watcher event
classifies as "in sync" and terminates.

### 5.2 Stage-1 handling of a dirty local file

A stage-1 node does not push. On any row where `D ≠ S` (a local edit it cannot
propagate), it MUST NOT silently overwrite: it MUST first copy the local content
to the conflict area (§6.1), log a warning, then converge. No user edit is ever
destroyed without a preserved copy, even before stage 2 lands.

### 5.3 Polling

Nodes poll the authority's manifest on an interval, using `If-None-Match`. A
host MAY add a change signal to trigger an early poll, but it is strictly a
latency optimization: it MUST NOT be required for correctness, and a lost signal
MUST NOT delay convergence beyond the poll interval.

### 5.4 Unrepresentable files

If a node cannot store an entry, it MUST record it as unsynced-with-reason, MUST
NOT retry in a tight loop, and MUST NOT advertise the path in its own manifest.
A node MUST NOT apply its own ignore globs or size caps to reject authority
content — that policy is the authority's (§2.1); local caps govern only what the
node itself originates.

## 6. Writes and conflicts

There is no automatic merge and no last-writer-wins tiebreak. CAS (§4.3) makes a
conflict an explicit, surfaced outcome:

1. A writer holds the digest it based its edit on (an API client from a read; a
   stage-2 watcher from its `synced_digest`).
2. It writes with `If-Match: <that digest>`.
3. If another write landed first, it fails `412` with the current entry.
   **Canonical state is untouched and nothing is lost** — the losing content is
   still on the loser's disk.
4. Resolution: an interactive client re-reads and re-applies. A **stage-2
   watcher has no human in the loop**, so it MUST copy the local content to the
   conflict area (§6.1), then converge to canonical, then surface the conflict.

A losing write MUST NOT mutate canonical state — no digest change, no
notification, no file created in the synced tree.

### 6.1 The conflict area

Preserved conflict copies MUST be stored **outside the knowledgebase tree** (a
node-local conflict directory), never as a sidecar inside it. A sidecar in the
tree would itself be synced — propagating one node's conflict to the whole
cluster — and would overwrite prior preserved copies. Conflict copies MUST be
uniquely named (path + timestamp + short digest) so repeated conflicts on one
path never clobber each other, and MUST be surfaced to the operator (log + an
API/event) since nothing in the tree reveals them.

### 6.2 Editing the authority's tree directly

A file edited directly on the authority bypasses CAS by nature — the filesystem
is the authority there and the last write to disk wins, as for any local file.
This is intentional and MUST be documented to operators: a direct edit on the
authority supersedes a concurrent API write.

## 7. Authority change

Because identity is the content digest (§2.2) and the manifest is complete
(§2.4), an authority change needs no special protocol handling: the new
authority serves the manifest of its own tree; participants poll, run §5.1, and
converge. No counters regress, no term is needed, no hand-off is required.

The real consequence is that the new authority's tree must itself be current.
How the canonical tree survives an authority change is a **host** concern
(replicating the tree, shared storage, or accepting that an elected authority
serves the last state it held), and MUST be stated by the host. A host that
cannot guarantee it MUST NOT enable KSP together with automatic failover.

## 8. Opt-in

KSP is opt-in. Disabled: no watcher, no convergence, and KSP routes return `501`
with `X-KSP-Enabled: false` (distinguishing "sync disabled" from "empty
knowledgebase"). The knowledgebase stays purely local — byte-for-byte the pre-KSP
behavior.

## 9. Authorization

Two principals reach KSP routes and need different rules:

- **A user principal** — reads require project **view** authority (owner or team
  member); writes require the host's project write authority. KB routes MUST NOT
  inherit a host's "project reads are open" policy: metadata redaction cannot
  redact document bytes, so an ungated read discloses project content.
- **A node principal** (cluster token) performing convergence — permitted to read
  the manifest and files **without** a per-user identity, because convergence is
  machine-initiated and has no user to attribute. A host whose project
  authorization normally requires an echoed user for node callers MUST provide
  this explicit convergence-read path, or sync fails closed once auth is enabled.
  A node principal MUST NOT get write access on that path; a stage-2 watcher's
  push is attributed to the editing user where the host can determine it, and
  otherwise to the node, and is subject to the project's write authority.

## 10. Security considerations

- **Path traversal.** §2.1 rejection applies on write *and* on pull: a node MUST
  re-validate a path from the manifest before writing, never trusting the
  authority blindly.
- **Size.** The authority enforces the cap on accept; a node SHOULD bound total
  local size rather than fill its disk.
- **Digest verification.** A node SHOULD verify pulled bytes against the entry
  digest and discard a mismatch.
- **Content trust.** KSP moves document bytes; it does not execute them. Agents
  acting on KB content do so under the host's tool-approval and filesystem
  permission layers.
- **Disclosure.** Every participating node stores project documents on disk;
  operators must treat those trees as project-confidential.

## 11. Staging

Both stages use the message set in §4 unchanged.

**Stage 1 — converge and read.** Participants poll, apply the clean rows of
§5.1, preserve dirty local files (§5.2), and serve local reads. Writes go
through the API and forward to the authority. Delivers one shared knowledgebase
visible and writable on every node.

**Stage 2 — symmetric multi-writer.** Participants watch their trees; a local
edit executes the push rows of §5.1 using `synced_digest` as the `If-Match`
base, with conflicts resolved per §6. Requires only: the watcher (already built
for the authority in stage 1), the push rows, a durable pending-change queue so
an edit made while disconnected replays on reconnect, and the conflict area.
**Delivers the destination in §1: every node's tree watched.**

Deliberately outside both stages: automatic merge of simultaneous edits to one
file (§1.1) — intrinsic to file-granular sync, and resolvable only by structured
or CRDT merge, which would be a protocol revision.
