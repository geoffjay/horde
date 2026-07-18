---
type: Pattern
title: Unit / integration test split via build tags
description: Keep the default test suite fast and deterministic; put subprocess, network, and timing tests behind a //go:build integration tag run manually via task test:integration.
tags: [pattern, testing, ci, build-tags]
timestamp: 2026-07-18T00:00:00Z
---

# Pattern

`task test` (`go test -race -count=1 ./...`) runs **unit tests only** — no
subprocess spawning, no real network/ports, no timing-dependent behaviour. It is
what CI runs, so it must stay fast and deterministic. The **exhaustive**
suite — subprocess agents, real memberlist gossip, multi-node clusters, and
streaming/lifecycle timing — lives behind the `//go:build integration` build tag
and runs only via `task test:integration` (which builds `bin/horde` first).

## When a test is "integration"

Tag a test `integration` (or split the flaky case out of a mixed file into a
`*_integration_test.go`) when it does any of:

- spawns a real subprocess (`SpawnAgent` → `horde agent`, `horde aap-mock`, an
  external adapter);
- binds a real port or joins a real memberlist ring;
- depends on goroutine scheduling / timing (`Eventually`, `time.Sleep`, an
  SSE stream reaching a client, a turn completing after a disconnect).

Pure in-process tests (config parsing, pipe-backed AAP sessions, `httptest`
handler assertions with no background goroutines, table-driven logic) stay in
the default unit suite.

## Mechanics

- First line of the file: `//go:build integration`, then a blank line, then
  `package …`. The file compiles only under `-tags=integration`.
- White-box tests stay `package server`; cross-package tests that import
  `internal/api` are `package server_test` (avoids an import cycle).
- Shared helpers used by both unit and integration tests live in an **untagged**
  file (available in both builds). Helpers used only by integration tests move
  into the tagged file so the unit build has no unused symbol.
- Binary-dependent tests call `findHordeBinary` / `findHordeBinaryLocal`, which
  `t.Skip` when `bin/horde` is absent.
- CI runs `task test` (unit) plus the docker-compose cluster job;
  `task test:integration` is manual.

## Why

Non-deterministic tests in the default path cause intermittent CI failures and
erode trust in the suite. Separating them keeps `task test` a reliable gate
while preserving an exhaustive, repeatable verification for integrated
behaviour — the same split that surfaced (and let us fix) a real SSE
header-flush deadlock in the event stream when the multi-node suite was first
run. See the [Phase 4 plan](../plans/phase-4-distributed.md) for the distributed
features the cluster suite covers.
