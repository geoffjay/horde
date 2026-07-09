---
okf_version: "0.1"
---

# horde knowledge base

This is the working knowledge base for the horde project, conforming to the
[Open Knowledge Format (OKF) v0.1](https://github.com/GoogleCloudPlatform/knowledge-catalog/blob/main/okf/SPEC.md).

It consolidates working knowledge about the project: what horde is, how it is
structured, decisions and their rationale, recurring patterns, and plans.
It is authored by people and agents and meant to be read by both.

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