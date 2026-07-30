---
type: Pattern
title: Three-way convergence for file-granular sync
description: Compare the authority's digest, a node's synced_digest, and the on-disk digest to classify every file path into exactly one convergence action — the correctness core of KSP v1.
tags: [pattern, knowledgebase, sync, distributed, convergence]
timestamp: 2026-07-29T00:00:00Z
---

# Three-way convergence for file-granular sync

The [KSP v1](/docs/spec/knowledgebase-sync-protocol-v1.md) convergence algorithm
is a pure three-way comparison per path. For each file, a node compares:

- **`A`** — the authority's entry digest (or `—` if absent from the manifest).
- **`S`** — the node's own `synced_digest` (or `—` if never synced).
- **`D`** — the on-disk digest (or `—` if the file doesn't exist locally).

The result is exactly one action from the normative table in KSP §5.1. This
table is complete — every combination of `(A, S, D)` maps to exactly one row —
and it is the correctness core of the protocol.

## Why three values, not two

Comparing only `A` vs `D` (authority vs disk) loses the distinction between "I
pulled this file" and "a user edited it." Without `S`, applying a pulled file
looks identical to a local edit, and the node cannot tell whether it should
push, pull, or do nothing. `synced_digest` is the pivot: it records what the node
last agreed with the authority, so `D ≠ S` means "changed locally since the last
sync" and `A ≠ S` means "changed remotely."

## The pure function

`classifyPath(A, S, D)` returns a `kbClassifyResult` with an action. The stage
distinction is a thin wrapper:

- **`classifyStage1`** — a non-pushing participant maps push rows to conflicts
  (KSP §5.2: a read-only node preserves local edits to the conflict area, then
  converges to canonical).
- **`classifyStage2`** — a pushing participant (WatchLocal enabled) executes the
  raw `classifyPath` result with no remapping: push rows push, conflicts
  conflict.

This makes the table the single source of truth — the stage only changes which
actions are *executed*, not how paths are *classified*.

## No sync loop

Applying a pulled file sets `S` to the pulled digest, so the resulting watcher
event (the write to disk) classifies as `A=S=D` → "in sync" → no action. The
loop terminates by construction; there is no feedback cycle.

## 304 handling for pushing nodes

A pushing node (stage 2) must classify local paths even when the authority's
manifest is unchanged (a 304 response). The converger reconstructs the
authority's entries from sync records — every path with a `synced_digest` was in
the last manifest at that digest — so `A=S` for known paths and any `D ≠ S`
classifies as a push. This is a safe lower bound: a path with `S` set and `D ≠ S`
triggers a push regardless of the exact authority digest.

## See also

* [KSP v1 §5.1](/docs/spec/knowledgebase-sync-protocol-v1.md) — the normative table.
* [Knowledgebase sync plan](../plans/knowledgebase-sync.md) — the six slices.
* [Knowledgebase sync decision](../decisions/knowledgebase-sync.md) — why this design.
