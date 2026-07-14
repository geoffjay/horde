---
okf_version: "0.1"
---

# horde knowledge base

This is the working knowledge base for the horde project, conforming to the
[Open Knowledge Format (OKF) v0.1](https://github.com/GoogleCloudPlatform/knowledge-catalog/blob/main/okf/SPEC.md).

It consolidates working knowledge about the project: what horde is, how it is
structured, decisions and their rationale, recurring patterns, and plans.
It is authored by people and agents and meant to be read by both.

## For agents (policy)

This section is the single source of truth for how agents (Claude Code,
opencode, and others) should use this knowledge base. Tooling injects it into
context automatically (Claude via a `SessionStart` hook, opencode via the
`instructions` config), so it does not depend on `AGENTS.md`/`CLAUDE.md` being
picked up.

**Consult before acting.** Before working on a task, scan the entries below and
read any concept/decision/pattern doc relevant to what you are about to change.
Prefer the recorded decision or pattern over re-deriving one. This index is the
map; read the specific doc on demand rather than guessing.

**Update after acting.** Update the knowledge base when a change would make an
existing entry wrong or leave a new fact unrecorded. In particular:

* Config keys, ports, services, or env vars → update
  [`docs/environment.md`](../environment.md) and
  [Environment](concepts/environment.md) / [Configuration](concepts/configuration.md).
* A new architectural decision or a change to node/cluster behaviour →
  add or update a [decision](decisions/index.md) and
  [Architecture](concepts/architecture.md).
* A new recurring convention → add a [pattern](patterns/index.md).
* A new command, agent, or capability → update the relevant concept doc and,
  if it shifts scope, the [roadmap](plans/roadmap.md).
* Work landing (a phase/slice starting or completing) → update that
  phase/slice's status in the [roadmap](plans/roadmap.md) and its detailed plan
  doc, and add a `log.md` entry. Status drift here is what makes the roadmap
  mislead — keep it current even when scope is unchanged.

Concept docs require YAML frontmatter with a `type` field; `index.md` and
`log.md` are reserved. When you add a doc, add a one-line pointer to the
matching category index below. If you deliberately decide *not* to record a
change, that is fine — the policy is judgement, not a mandate to touch the KB on
every edit.

## Concepts

* [Architecture](concepts/architecture.md) - the horde node, master/slave modes, and agent subprocess model.
* [Configuration](concepts/configuration.md) - the layered config system, search paths, and env overrides.
* [Environment](concepts/environment.md) - ports, services, and environment variables.
* [Agent model](concepts/agent-model.md) - how ADK agents are defined, hosted as subprocesses, and invoked over HTTP on unix sockets.
* [Agent Adapter Protocol (AAP)](concepts/agent-adapter-protocol.md) - vendor-neutral NDJSON protocol for driving external coding agents via adapters.
* [Agent execution context](concepts/agent-execution-context.md) - queryable per-agent work-state (project, issue, blocked, waiting, errors, approvals).

## Decisions

* [Use bubbletea + lipgloss, not crush](decisions/use-bubbletea-not-crush.md) - crush is an app, not a TUI library.
* [Vendor plantd config into internal/config](decisions/vendor-config.md) - avoid pulling plantd/core as a dependency.
* [logrus for logging](decisions/logrus-for-logging.md) - logging library choice.
* [Master/slave cluster model](decisions/master-slave-model.md) - distributed topology and its trade-offs.
* [HTTP + SSE for the node API transport](decisions/http-api-transport.md) - HTTP/JSON + SSE over chi (net/http-native), in-process event bus now, brokerless messaging deferred to Phase 4.
* [HTTP over unix domain sockets for agent invocation](decisions/agent-invocation-transport.md) - agent subprocess serves a local HTTP API on a unix socket; the node reverse-proxies invoke requests.
* [The TUI consumes the node API](decisions/tui-uses-node-api.md) - the TUI always goes over the API; it does not start a node and retries with a 60s countdown.
* [Adopt the Agent Adapter Protocol (AAP)](decisions/agent-adapter-protocol.md) - own a vendor-neutral, agentd-compatible NDJSON host↔adapter protocol for external coding agents; permissions/multi-user stay above it.
* [Project, team, and user model](decisions/project-team-user-model.md) - what a project, team, and user are; the 3.5a/3.5b split that defers per-user auth.

## Patterns

* [One file per cobra command](patterns/one-file-per-command.md) - cmd/ package layout.
* [Config extension pattern](patterns/config-extension.md) - embedding generic config + app-specific sections.
* [Subprocess agent hosting](patterns/subprocess-agent-hosting.md) - the binary hosts its own agents.
* [TUI status line and command palette](patterns/tui-status-line-and-palette.md) - configurable status blocks + a ctrl+p palette over a dimmed background.
* [Test file naming](patterns/test-file-naming.md) - name test files after what they test, not after phases or milestones.

## Plans

* [Roadmap](plans/roadmap.md) - phasing of horde capabilities.
* [Phase 2 — Server API](plans/phase-2-server-api.md) - node API transport, event streaming, slave↔master contract.
* [Phase 3 — Agent mechanism](plans/phase-3-agents.md) - long-lived agent subprocesses invoked over HTTP on unix sockets, streaming, resume, agent registry.
* [Agent execution context](plans/agent-execution-context.md) - queryable per-agent work-state, materialized at the node from AAP signals, aggregated across the cluster with redacted remote access.
* [Projects, teams, and multi-turn context](plans/projects-teams.md) - Phase 3.5 Slice B: project/team data model, project API, agent assignment, session-key derivation.

## References

* [OKF spec](references/okf-spec.md) - pointer to the OKF v0.1 specification.
