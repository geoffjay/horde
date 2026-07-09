---
type: Plan
title: Improvement Tasks
description: Outstanding code-review follow-ups not yet addressed.
tags: [plan, tasks, tech-debt]
timestamp: 2026-07-09T00:00:00Z
---

# Improvement Tasks

Follow-ups from the 2026-07-09 code review. The two concurrency fixes
(`Server.nextID` race, `config.Get` double-checked-locking race) and wiring up
`config.Config.Validate` were completed at review time; the items below remain.

## Correctness / design

### Agent `name` is a no-op end to end

`agents.New` (`agents/agents.go`) always builds the `greeter` and ignores any
name. Yet `Server.SpawnAgent(ctx, name)` (`internal/server/server.go`) takes a
`name`, uses it only for logging, and spawns `horde agent --name <name>` — and
`cmd/agent.go`'s `runAgent` also ignores `--name` and builds the greeter. The
`--name` flag and the `name` parameter are therefore cosmetic.

**Task:** introduce a real agent registry — e.g. `agents.New(name)` that
dispatches by name and returns an error for unknown agents — and thread it
through `SpawnAgent` and `runAgent`. Until then, consider dropping the unused
`name` plumbing to avoid a misleading API. (Overlaps with Roadmap Phase 3.)

### TUI path skips `Server.Run` graceful teardown

~~`cmd/tui.go` calls `srv.Start()` but never `srv.Run()`, so the grace-period →
`Kill()` fallback in `Server.Run` (`internal/server/server.go`) never executes
on the TUI path. Teardown relies solely on the per-proc `ctx.Done()` goroutine
in `SpawnAgent`, which sends SIGINT but never force-kills a wedged agent.~~

**Resolved in Phase 2:** the TUI no longer starts or owns a server, so there
is no TUI teardown path to unify. Agent teardown is solely `Server.Run`'s
job on the `horde serve` path.

### Slave "connected" status is fake

~~`connectLeader` (`internal/server/server.go`) sets `leaderOK = true`
immediately with no real connection, and the TUI renders "leader connected" as
truth.~~

**Resolved in Phase 2:** `connectLeader` now uses a real `leaderClient`
(`internal/server/leaderclient.go`) that performs `POST /cluster/register`
then loops on `GET /cluster/heartbeat`; `leaderOK` reflects the actual
round-trip outcome. `TestStart_SlaveBecomesLeaderConnected` was rewritten
against an `httptest` master stub.

## Robustness

### `config.Get` still calls `log.Fatalf`

`config.Get` (`internal/config/horde.go`) exits the process on a load/validate
error. `cmd/cli.go`'s `init` already calls `config.Load()` (ignoring its error),
which is the natural place to surface configuration problems.

**Task:** make `Get` non-fatal (return the cached config, which `Load` has
already validated) and report the error from an explicit `Load` call at startup
instead of via `os.Exit` deep in the call graph.

### TUI never auto-refreshes

~~`refreshAgents` (`internal/app/app.go`) fires once in `Init` and then only
on the `r` key. When an agent subprocess exits it is removed from `procs`, but
the view does not reflect it until the user refreshes manually.~~

**Resolved in Phase 2:** the rewritten TUI (`internal/app/app.go`) polls the
node API every 2 seconds via `tea.Tick` while connected, so agent
lifecycle changes surface without manual refresh.

## Cleanup

### Remove dead `MarshalConfig`

`MarshalConfig` (`internal/config/config.go`) has no callers in source or tests.

**Task:** delete it (or add a caller if it is intended for a future feature).

### `LoadConfig` duplicates `prepare()`

`LoadConfig` (`internal/config/config.go`) inlines the same ~30-line
config-file-resolution block that `prepare()` already provides and that
`LoadConfigWithDefaults` reuses. `LoadConfig` is used only by one test and does
not apply defaults or validate.

**Task:** have `LoadConfig` call `prepare()`, or drop it since production code
only uses `LoadConfigWithDefaults`.
