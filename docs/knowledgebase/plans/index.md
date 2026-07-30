# Plans

Forward-looking plans for the project.

* [Roadmap](roadmap.md) - phasing of horde capabilities.
* [Improvement Tasks](improvements.md) - outstanding code-review follow-ups.
* [Agent execution context](agent-execution-context.md) - queryable per-agent work-state, materialized at the node and aggregated across the cluster with redacted remote access.
* [TUI for projects, teams, and execution context](tui-projects.md) - complete; the domain TUI over projects, teams, execution context, invoke, and cluster (navigation since superseded by the [sidebar](../patterns/tui-sidebar-navigation.md)).
* [Phase 3.5b — Per-user auth, ownership, and permissions](phase-3.5b-auth.md) - complete (all five slices); config-token per-user auth, project ownership, owner+team authorization, per-user tool/permission scopes (opt-in, OS sandboxing deferred).
* [Distributed project management](distributed-project-management.md) - forward project mutations from worker to coordinator, and add a `horde project` CLI subcommand.
* [Leader failover](leader-failover.md) - automatic leader failover via raft on the gossip ring, coordinator-only state replicated through the raft log, and a stable entry point that survives a leader change (Phase 5, complete).
* [Knowledgebase sync](knowledgebase-sync.md) - planned (roadmap phase 6); share each project's `.horde/knowledgebase/` across the cluster, reaching **symmetric multi-writer** (every node's tree watched) in two stages — authority-serialized writes, content-digest identity, three-way convergence, and compare-and-swap. Scope-parameterized (project ships; team/user/cluster are later registrations). Opt-in; wire format in [KSP v1](/docs/spec/knowledgebase-sync-protocol-v1.md).
