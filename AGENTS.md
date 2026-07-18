# AGENTS.md

Go 1.26 project. Build with `go build .` (binary: `./bin/horde` via Taskfile).

## Commands

- **Default flow:** `task` (runs tidy → fmt → vet → test → build).
- **Test (unit):** `task test` or `go test -race -count=1 ./...` — fast, deterministic, no binary/network. This is what CI runs.
- **Test (integration):** `task test:integration` — the exhaustive suite (subprocess agents + multi-node cluster) behind the `//go:build integration` tag; it builds `bin/horde` first. Run manually to verify integrated behaviour. Never in the default `task`/CI test path.
- **Test (one package):** `go test -race ./internal/config/...`
- **Test (one test):** `go test -race -run TestNew_DefaultsToMaster ./internal/server/...`
- **Lint:** `task lint` (`golangci-lint run --timeout=5m`). Must report 0 issues before done.
- **Format:** `task fmt` (gofmt -s + goimports). CI fails on unformatted files.
- **Tidy:** `task tidy`; CI's lint workflow rejects an untidy `go.mod`/`go.sum`.
- **Docker cluster:** `task docker:up` (master + 2 slaves), `task docker:down`, `task docker:logs`.
- **Release snapshot:** `task snapshot` (goreleaser `--snapshot`, no publish/tap push).
- **Release:** `task release` (runs goreleaser; publish to GitHub + homebrew-tap). Driven by `.github/workflows/release.yml` on `v*` tag pushes. The first release will be `v0.1.0`.

Required order when changing code: fmt → vet → lint → test → build. The lint job in `.github/workflows/lint.yml` enforces gofmt, `go mod tidy` diff, and golangci-lint.

## Architecture

- Entry point is root `main.go` → `cmd.Execute()`. The `cmd/` package has **one file per cobra command** (`cli.go` root + `Execute`, `serve.go`, `tui.go`, `agent.go`, `daemonize.go`). Add new subcommands in their own file, registered via `rootCmd.AddCommand` in that file's `init()`.
- `horde` (no subcommand) launches the TUI; `horde serve --mode master|slave` runs a node (`master` default); `horde agent` is **hidden**, spawned by the server as a subprocess to host one ADK agent.
- Agents live in the top-level `agents/` package and are built on `google.golang.org/adk/v2` (the V2 ADK). The binary hosts its own agents as subprocesses of itself.
- Config is vendored from `plantd/core/config` into `internal/config/` (adapted to the `HORDE_` prefix). Do **not** add `plantd/core` as a dependency.

## Conventions that differ from defaults

- **Logging:** `github.com/sirupsen/logrus` (not `log`/`slog`). Formatter/level come from the `log` config section. **No Loki** — do not add Loki hooks/config despite the upstream plantd config having them.
- **Config env prefix:** `HORDE_*` (dots become underscores, e.g. `HORDE_SERVER_PORT`).
- **Config loader:** a missing config file is **not** fatal — defaults + env overrides still apply. Preserve this when editing `internal/config/config.go`.
- **TUI:** `charm.land/bubbletea/v2` + `charm.land/lipgloss/v2`. `github.com/charmbracelet/crush` is a standalone app, **not** a reusable library — never import it.
- **Model receivers:** the bubbletea `Model` in `internal/app/app.go` uses **pointer** receivers (satisfies `tea.Model` and avoids a gocritic hugeParam lint).
- **No `gochecknoinits`:** cobra commands legitimately register flags/subcommands in `init()`. This linter is disabled in `.golangci.yml`.

## Tests

- Uses `github.com/stretchr/testify` (assert/require). Table-driven where possible.
- **No phase/milestone references in code:** name and describe code by what it is, not by the phase/plan/issue that introduced it — in file names, comments, and identifiers. `context_test.go`, not `phase3_test.go`; "falls back to per-invocation sessions", not "Phase 3 fallback". See [no phase/milestone references](docs/knowledgebase/patterns/no-phase-references.md).
- `internal/config` tests load fixtures from `internal/config/testdata/` (yaml/json/toml). They set `HORDE_CONFIG` and call `config.Reset()` to clear the singleton — **always call `Reset()` before relying on `Get()`** in a test.
- `internal/server` tests set `SpawnDefaultAgent: false` in `server.Config` to avoid spawning real subprocesses. Keep doing this; do not spawn the `horde agent` subprocess from unit tests.
- **Unit vs integration split (`//go:build integration`):** heavy or non-deterministic tests — subprocess spawn (`spawn_test.go`, `aaphost_integration_test.go`, `project_integration_test.go`), real memberlist (`gossip_integration_test.go`), network/goroutine lifecycle (`server_integration_test.go`, `integration_test.go`), and the multi-node cluster suite (`cluster_integration_test.go`) — carry the `//go:build integration` tag and run only under `task test:integration`. The default `task test` (and CI) compiles/runs unit tests only, so it stays fast and deterministic. When adding a test that spawns a subprocess, binds a real port, or depends on timing, tag it `integration` (or move the flaky case into the matching `*_integration_test.go`) rather than leaving it in the unit path. Integration files that need the binary reuse `findHordeBinary`/`findHordeBinaryLocal`, which skip when `bin/horde` is absent.
- `TestStart_SlaveBecomesLeaderConnected` (now in `server_integration_test.go`) relies on a goroutine marking `leaderOK`; if you change `connectLeader`, keep the background + non-blocking contract.

## Gotchas

- `.golangci.yml` has `run.tests: false` — lint doesn't scan `_test.go` except via the `mnd` exclusion rule. Test files are still vetted by `go vet`.
- The binary builds its own agent subprocesses via `os.Executable()`; running `go run . agent` won't behave like the real binary path. Build first (`task build`) for subprocess-related testing.
- `cmd/daemonize.go` re-execs the binary with `setsid`; it's `nolint:noctx` by design (a context would kill the daemon on return). Preserve the nolint comment if you edit it.
- `server.go`'s `exec.CommandContext` call is `nolint:gosec` (G204) because `AgentCommand` is operator-controlled config, not untrusted input. Don't remove the nolint without replacing the rationale.

## Releases

Releases are driven by goreleaser via `.goreleaser.yml` and `.github/workflows/release.yml` (triggers on `v*` tag pushes and manual `workflow_dispatch`). `task snapshot` runs a local non-publishing build; `task release` runs the real thing locally.

- The `HOMEBREW_TAP_TOKEN` repo secret (a PAT with write access to `geoffjay/homebrew-tap`) is required for the `brews` block to push the formula. The job logs a notice and skips the tap push if it's unset.
- `brews:` (formulas) is deprecated in goreleaser v2.10+ in favour of `homebrew_casks`, but casks are for macOS GUI apps and don't support Linux. horde targets linux + darwin as a CLI, so `brews:` is correct here; `goreleaser check` reports a deprecation warning but `goreleaser release` still works. Re-evaluate if a future goreleaser major removes `brews:`.
- `cmd.version` is injected at build time via `-ldflags "-X github.com/geoffjay/horde/cmd.version={{.Version}}"`; it defaults to `"dev"` for plain `go build`. The `--version` flag exists mainly so the Homebrew formula's `test do` block can verify the install.

## Knowledge base

`docs/knowledgebase/` conforms to [OKF v0.1](https://github.com/GoogleCloudPlatform/knowledge-catalog/blob/main/okf/SPEC.md). Concept docs require YAML frontmatter with a `type` field; `index.md`/`log.md` are reserved. Update `docs/environment.md` (ports/env vars) and the knowledge base when adding config keys or services.

**Consult/update policy for agents** lives in `docs/knowledgebase/index.md` ("For agents (policy)") — that file is the single source of truth. It is injected into context automatically so it does not depend on this file being read:

- **Claude Code:** `.claude/hooks/kb-inject.py` (a `SessionStart` hook) injects the KB index every session; `.claude/hooks/kb-reminder.py` nudges on `PostToolUse` and, once per session, on `Stop` when KB-relevant files changed but the KB was not updated. Neither hard-blocks edits.
- **opencode:** the `instructions` array in `.opencode/opencode.jsonc` lists `docs/knowledgebase/index.md`.

Edit the policy in the index; the tooling picks it up from there.
