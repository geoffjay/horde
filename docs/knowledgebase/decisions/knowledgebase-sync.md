---
type: Decision
title: Knowledgebase sync — authority-serialized multi-writer
description: Share each project's per-project OKF knowledgebase across the cluster with authority-serialized writes, content-digest identity, three-way convergence, and compare-and-swap — reaching symmetric multi-writer in two stages on one wire protocol. Pull-based, not event-push; scope-parameterized while only project ships.
tags: [decision, knowledgebase, sync, distributed, cluster]
timestamp: 2026-07-29T00:00:00Z
---

# Context

The [persistence-and-knowledgebase decision](persistence-and-knowledgebase.md)
§4 named the distributed knowledgebase "the hardest problem" and deferred it:
Slice B creates the per-project `.horde/knowledgebase/` tree but does not sync
it. Through Phase 5 the cluster replicates only project/team metadata and AAP
resume tokens (raft); `Project.Workspace` is a path string, never file content.
An earlier draft of KB sync proposed a bidirectional, optimistically-versioned,
event-push design. Review found it unsound — lost updates on apply, offline
edits that could never propagate, version regression across failover,
unreachable tiebreak rules, and a tombstone black hole that swallowed recreates.

# Decision

Adopt **authority-serialized multi-writer** over a **pull-based** convergence
model, with **content-digest identity**, **three-way convergence**, and
**compare-and-swap** writes. Delivered in two stages on one wire protocol
([KSP v1](/docs/spec/knowledgebase-sync-protocol-v1.md)).

## Why authority-serialized, not peer-to-peer

Exactly one node (the cluster leader for `project` scope) is the authority for a
scope's canonical tree. Every write is serialized through it via CAS. This makes
"who wins" unambiguous with no tiebreak rule: a write that loses its CAS is an
explicit `412`, never a silent merge or an arbitrary last-writer-wins.

## Why content-digest identity, not version counters

Digests are authority-independent: a leader change cannot reorder, regress, or
corrupt sync state. There are no counters, no terms, no manifest hand-off. The
manifest is complete (not incremental): a node that misses any number of updates
converges on its next poll. No event stream to miss, no gap detection, no
acknowledgement tracking.

## Why `synced_digest` is the pivot

Each node tracks `synced_digest` separately from disk — the digest it last
agreed with the authority. Comparing it against the on-disk digest is how a node
distinguishes "changed because I pulled it" from "changed because a user edited
it." This distinction is what makes multi-writer safe; its absence causes silent
lost updates. Maintained from stage 1, where it also lets a read-only node
detect and preserve an unexpected local edit rather than destroy it.

## Why pull, not event-push

The event bus fans *in*, not out (`forwardEvents` is slave→master; there is no
outbound push). It is lossy by design (drops on a full subscriber channel), safe
for agent lifecycle events only because heartbeat digests re-derive state. KB
content is project metadata, not safe to propagate on the closed `server.Event`
struct. Polling keeps every call in the node→leader direction the codebase
supports, makes missed updates impossible by construction, and leaves `Event`
untouched. A change signal to trigger an early poll is a latency optimization
only (implemented in stage 2 via the participant watcher signaling the
converger).

## Why scope-parameterized while only project ships

The replicated unit is a scope `{kind, id}` — a label on the manifest and a key
in the route, never an input to convergence. Only `project` is registered; the
algorithm beneath is identical for any other kind. Registering a second kind
(`team`, `user`, `cluster`) is a registration (four bindings: identity,
authority, location, authorization), not a protocol revision or a route
migration. See the plan's [Future scopes](../plans/knowledgebase-sync.md#future-scopes)
for what each is blocked on.

## Why two stages on one wire protocol

Stage 1 makes the KB shared, readable, and writable by API on every node.
Stage 2 turns on each node's watcher so local file edits propagate — the
symmetric multi-writer destination. The wire protocol is the same for both: a
stage-2 watcher calls the identical CAS write endpoint a stage-1 API client
calls. Stage 1 is genuinely stage one of the destination, not a detour.

Stage 2's key insight: a 304 (manifest unchanged) does not mean "nothing to do"
for a pushing node — local edits still need to be pushed. The converger
reconstructs the authority's entries from sync records on a 304 and classifies
local paths to find push rows. Offline edits stay on disk with `D ≠ S`; the
three-way comparison + persisted sync records are the replay mechanism — on
reconnect the next poll classifies and pushes.

# Consequences

* Opt-in: `knowledgebase.sync.enabled` (default false). Disabled ⇒
  byte-for-byte current (local, git-backed) behavior.
* `watch_local` (default false) is the stage-2 switch: a participant with it set
  watches its local tree and pushes edits.
* Every participating node stores the scope's documents on disk; treat those
  trees and the conflict area as project-confidential.
* The one limitation that persists past stage 2: simultaneous edits to the *same
  file* cannot be auto-merged — the loser gets a preserved copy in the conflict
  area outside the tree. Intrinsic to file-granular sync; only structured/CRDT
  merge avoids it, and that would be a protocol revision.
* Conflict copies are surfaced via `GET /api/v1/kb/{kind}/{id}/conflicts` and in
  logs, since nothing in the synced tree reveals them.

# See also

* [Knowledgebase Sync Protocol v1](/docs/spec/knowledgebase-sync-protocol-v1.md)
  — the wire format (finalized).
* [Knowledgebase sync plan](../plans/knowledgebase-sync.md) — the six slices.
* [Data persistence and per-project knowledgebase](persistence-and-knowledgebase.md)
  — §4 named this the hardest problem and deferred sync; this fills that gap.
