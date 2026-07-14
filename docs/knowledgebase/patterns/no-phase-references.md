---
type: Pattern
title: No phase/milestone references in code
description: Name and describe code by what it is, not by the phase, plan, or issue that introduced it — in file names, comments, and identifiers.
tags: [pattern, conventions, naming, comments]
timestamp: 2026-07-13T00:00:00Z
---

# Pattern

Refer to code by **what it is or does**, never by the phase, plan, milestone,
or issue that introduced it. This applies to file names, comments, identifiers,
and any other durable text in the codebase.

A reader arriving after the work is done has no access to the plan. "Phase 3,"
"Slice B," or "the MVP" are points in a process that is over; they tell that
reader nothing and become actively misleading once the plan doc is gone or
renumbered. Describe the behaviour instead.

## Rules

* **File names.** Name test and source files after the concept or unit they
  cover. `context_test.go`, not `phase3_test.go`; `spawn_test.go`, not
  `slice_a_test.go`.
* **Comments.** Describe the actual behaviour, not its origin. Write
  "falls back to per-invocation sessions (no conversation continuity)", not
  "falls back to Phase 3 semantics." If removing the phase reference would make
  the comment less useful, that is the signal the comment was leaning on
  knowledge the reader does not have — state that knowledge directly.
* **Identifiers.** No `phase3Handler`, `sliceBConfig`, `mvpMode`. Name for the
  capability.
* **One concept per file.** If a test file grows to cover multiple unrelated
  concepts, split it by concept (`server_test.go` for the Server struct,
  `spawn_test.go` for subprocess lifecycle, `context_test.go` for execution
  context materialization).
* **Integration tests** that span packages use the external test package
  (`package server_test`) and are named after the integration seam:
  `integration_test.go`, `spawn_test.go`.

# Rationale

Code is read far more often than it is written, and it outlives the plan that
produced it. A name or comment that encodes a point in time (`phase3_test.go`,
"Phase 3 fallback") goes stale the moment that phase ends and communicates
nothing to a reader who arrives later — including agents, who see the code but
not the plan execution. Naming and describing by concept stays accurate for the
life of the codebase and makes code discoverable by grep or directory listing.
