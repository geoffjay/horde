# Plans

Forward-looking plans for the project.

* [Roadmap](roadmap.md) - phasing of horde capabilities.
* [Improvement Tasks](improvements.md) - outstanding code-review follow-ups.
* [Agent execution context](agent-execution-context.md) - queryable per-agent work-state, materialized at the node and aggregated across the cluster with redacted remote access.
* [TUI for projects, teams, and execution context](tui-projects.md) - complete; the domain TUI over projects, teams, execution context, invoke, and cluster (navigation since superseded by the [sidebar](../patterns/tui-sidebar-navigation.md)).
* [Phase 3.5b — Per-user auth, ownership, and permissions](phase-3.5b-auth.md) - complete (all five slices); config-token per-user auth, project ownership, owner+team authorization, per-user tool/permission scopes (opt-in, OS sandboxing deferred).
* [Distributed project management](distributed-project-management.md) - forward project mutations from slave to master, and add a `horde project` CLI subcommand.
* [Leader failover](leader-failover.md) - automatic leader failover via raft on the gossip ring, master-only state replicated through the raft log, and a stable entry point that survives a leader change (Phase 5, complete).
* [Phase 6 — Knowledgebase sync](phase-6-knowledgebase-sync.md) - planned; synchronize each project's `.horde/knowledgebase/` across cluster nodes (file watch, leader-authoritative file-content replication over HTTP, join/leave reconciliation, LWW conflict resolution). Opt-in; wire format in [KSP v1](/docs/spec/knowledgebase-sync-protocol-v1.md).
