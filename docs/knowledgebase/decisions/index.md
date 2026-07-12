# Decisions

Records of significant decisions and their rationale.

* [Use bubbletea + lipgloss, not crush](use-bubbletea-not-crush.md) - crush is an app, not a TUI library.
* [Vendor plantd config into internal/config](vendor-config.md) - avoid pulling plantd/core as a dependency.
* [logrus for logging](logrus-for-logging.md) - logging library choice.
* [Master/slave cluster model](master-slave-model.md) - distributed topology and its trade-offs.
* [HTTP + SSE for the node API transport](http-api-transport.md) - HTTP/JSON + SSE, in-process event bus now, brokerless messaging deferred to Phase 4.
* [HTTP over unix domain sockets for agent invocation](agent-invocation-transport.md) - agent subprocess serves a local HTTP API on a unix socket; the node reverse-proxies invoke requests.
* [The TUI consumes the node API](tui-uses-node-api.md) - the TUI always goes over the API, no in-process shortcut.
* [Adopt the Agent Adapter Protocol (AAP)](agent-adapter-protocol.md) - own a vendor-neutral, agentd-compatible NDJSON host↔adapter protocol for external coding agents; permissions/multi-user stay above it.
* [Project, team, and user model](project-team-user-model.md) - what a project, team, and user are; how they relate; the 3.5a/3.5b split that defers per-user auth.
