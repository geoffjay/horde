# Knowledge Base Update Log

## 2026-07-08
* **Initialization**: Created the knowledge base structure conforming to OKF v0.1.
* **Creation**: Added root [index](/docs/knowledgebase/index.md).
* **Creation**: Added [architecture](/docs/knowledgebase/concepts/architecture.md), [configuration](/docs/knowledgebase/concepts/configuration.md), [environment](/docs/knowledgebase/concepts/environment.md), and [agent model](/docs/knowledgebase/concepts/agent-model.md) concepts.
* **Creation**: Added decisions for [TUI library](/docs/knowledgebase/decisions/use-bubbletea-not-crush.md), [config vendoring](/docs/knowledgebase/decisions/vendor-config.md), [logrus](/docs/knowledgebase/decisions/logrus-for-logging.md), and [master/slave model](/docs/knowledgebase/decisions/master-slave-model.md).
* **Creation**: Added patterns for [command layout](/docs/knowledgebase/patterns/one-file-per-command.md), [config extension](/docs/knowledgebase/patterns/config-extension.md), and [subprocess agents](/docs/knowledgebase/patterns/subprocess-agent-hosting.md).
* **Creation**: Added the [roadmap](/docs/knowledgebase/plans/roadmap.md) and an [OKF spec reference](/docs/knowledgebase/references/okf-spec.md).

## 2026-07-13
* **Update**: Marked [Phase 3.5 Slice A — Agent execution context](/docs/knowledgebase/plans/agent-execution-context.md) complete in the [roadmap](/docs/knowledgebase/plans/roadmap.md) (data model, node materialization, local query/stream API, cross-node aggregation, redaction); Phase 3.5 Slice B is the next planned work.
* **Creation**: Added the [Slice B plan — Projects, teams, and multi-turn context](/docs/knowledgebase/plans/projects-teams.md) (project/team data model, project API, agent-to-project assignment, session-key derivation); linked it from the [roadmap](/docs/knowledgebase/plans/roadmap.md) and the KB [index](/docs/knowledgebase/index.md).
* **Update**: Clarified the no-active-project case in the [project/team/user model decision](/docs/knowledgebase/decisions/project-team-user-model.md) — an agent with no active project remains invokable and falls back to Phase 3 per-invocation sessions (projects are additive, not a gate); updated the [Slice B plan](/docs/knowledgebase/plans/projects-teams.md) to match (removed the 409, documented the fallback).
* **Creation**: Added the [data persistence and per-project knowledgebase decision](/docs/knowledgebase/decisions/persistence-and-knowledgebase.md) — XDG on-disk layout (`~/.config/horde`, `~/.local/share/horde`, `~/.local/state/horde`), the JSON KV / database / per-project storage split, per-project `.horde/` config overriding global, and the OKF knowledgebase as every project's shared brain (sync deferred to Phase 3.6/4); linked it from the [Slice B plan](/docs/knowledgebase/plans/projects-teams.md) and KB [index](/docs/knowledgebase/index.md).
* **Update**: Added `project.*` config section (workspace_dir, context_retention) and `paths.*` XDG directory resolution (config_dir, data_dir, state_dir with `HORDE_PATHS_*` env overrides) to `internal/config`; wired `DataPaths` into `server.Config` and `cmd/serve.go` with `EnsureDataDirs` at startup; updated `docs/environment.md`.
