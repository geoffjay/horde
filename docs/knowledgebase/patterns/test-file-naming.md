---
type: Pattern
title: Test file naming
description: Name test files after what they test, not after phases or milestones.
tags: [pattern, testing, conventions]
timestamp: 2026-07-13T00:00:00Z
---

# Pattern

Name test files after the **concept or unit** they test, not after the phase
or milestone that introduced them. A reader looking at `internal/server/`
should be able to find the tests for agent spawning by seeing a file named
`spawn_test.go`, not by knowing that agent spawning was introduced in
"Phase 3."

## Rules

* **Match the source file or concept.** If the code under test lives in
  `context.go`, the test file is `context_test.go`. If the tests cover a
  cross-cutting concept (e.g. subprocess spawning), name the file after
  that concept: `spawn_test.go`.
* **Never use phase, milestone, or roadmap names.** `phase3_test.go`,
  `slice_a_test.go`, `mvp_test.go` — all meaningless to a reader who arrives
  after the phase is complete.
* **One concept per file.** If a file grows to cover multiple unrelated
  concepts, split it. `server_test.go` for the Server struct's core
  behaviour; `spawn_test.go` for subprocess lifecycle; `context_test.go`
  for execution context materialization.
* **Integration tests** that span packages use the `_test.go` external test
  package (`package server_test`) and are named after the integration seam:
  `integration_test.go` for cluster integration, `spawn_test.go` for
  subprocess integration.

# Rationale

Test files are read more often than they are written. A name like
`phase3_test.go` encodes a point in time that becomes stale the moment the
phase ends, and it tells a new reader nothing about what the file actually
tests. Naming by concept stays accurate for the life of the codebase and
makes files discoverable by grep or directory listing.
