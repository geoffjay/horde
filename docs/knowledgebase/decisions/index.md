# Decisions

Records of significant decisions and their rationale.

* [Use bubbletea + lipgloss, not crush](use-bubbletea-not-crush.md) - crush is an app, not a TUI library.
* [Vendor plantd config into internal/config](vendor-config.md) - avoid pulling plantd/core as a dependency.
* [logrus for logging](logrus-for-logging.md) - logging library choice.
* [Master/slave cluster model](master-slave-model.md) - distributed topology and its trade-offs.
* [HTTP + SSE for the node API transport](http-api-transport.md) - HTTP/JSON + SSE, in-process event bus now, brokerless messaging deferred to Phase 4.
* [The TUI consumes the node API](tui-uses-node-api.md) - the TUI always goes over the API, no in-process shortcut.
