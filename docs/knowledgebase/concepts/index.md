# Concepts

Core knowledge about what horde is and how it is built.

* [Architecture](architecture.md) - the horde node, master/slave modes, and agent subprocess model.
* [Configuration](configuration.md) - the layered config system, search paths, and env overrides.
* [Environment](environment.md) - ports, services, and environment variables.
* [Agent model](agent-model.md) - how ADK agents are defined, hosted, and invoked.
* [Agent Adapter Protocol (AAP)](agent-adapter-protocol.md) - the vendor-neutral NDJSON protocol for driving external coding agents via adapters.
* [Agent execution context](agent-execution-context.md) - queryable per-agent work-state (project, issue, blocked, waiting, errors, approvals), materialized from AAP signals.
