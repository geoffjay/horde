# Plans

Forward-looking plans for the project.

* [Roadmap](roadmap.md) - phasing of horde capabilities.
* [Improvement Tasks](improvements.md) - outstanding code-review follow-ups.
* [Agent execution context](agent-execution-context.md) - queryable per-agent work-state, materialized at the node and aggregated across the cluster with redacted remote access.
* [TUI for projects, teams, and execution context](tui-projects.md) - complete; breadcrumb-navigated TUI over projects, teams, execution context, invoke, and cluster.
* [Distributed project management](distributed-project-management.md) - forward project mutations from slave to master, and add a `horde project` CLI subcommand.
* [Leader failover](leader-failover.md) - automatic leader failover via raft on the gossip ring, master-only state replicated through the raft log, and a stable entry point that survives a leader change (Phase 5, planned).
