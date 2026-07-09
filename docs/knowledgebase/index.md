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

Concept docs require YAML frontmatter with a `type` field; `index.md` and
`log.md` are reserved. When you add a doc, add a one-line pointer to the
matching category index below. If you deliberately decide *not* to record a
change, that is fine — the policy is judgement, not a mandate to touch the KB on
every edit.

## Concepts

* [Architecture](concepts/architecture.md) - the horde node, master/slave modes, and agent subprocess model.
* [Configuration](concepts/configuration.md) - the layered config system, search paths, and env overrides.
* [Environment](concepts/environment.md) - ports, services, and environment variables.
* [Agent model](concepts/agent-model.md) - how ADK agents are defined, hosted, and invoked.

## Decisions

* [Use bubbletea + lipgloss, not crush](decisions/use-bubbletea-not-crush.md) - crush is an app, not a TUI library.
* [Vendor plantd config into internal/config](decisions/vendor-config.md) - avoid pulling plantd/core as a dependency.
* [logrus for logging](decisions/logrus-for-logging.md) - logging library choice.
* [Master/slave cluster model](decisions/master-slave-model.md) - distributed topology and its trade-offs.

## Patterns

* [One file per cobra command](patterns/one-file-per-command.md) - cmd/ package layout.
* [Config extension pattern](patterns/config-extension.md) - embedding generic config + app-specific sections.
* [Subprocess agent hosting](patterns/subprocess-agent-hosting.md) - the binary hosts its own agents.

## Plans

* [Roadmap](plans/roadmap.md) - phasing of horde capabilities.

## References

* [OKF spec](references/okf-spec.md) - pointer to the OKF v0.1 specification.
