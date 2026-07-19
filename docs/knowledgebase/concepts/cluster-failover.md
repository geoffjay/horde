---
type: Concept
title: Cluster leader failover
description: What automatic leader failover would require, and why horde stops at gossip discovery for now.
tags: [cluster, distributed, failover, gossip, future]
timestamp: 2026-07-17T00:00:00Z
---

horde's master is **statically designated** (`horde serve --mode master`). It is
the single source of truth for cluster-wide state and the entry point clients
talk to. Slaves *discover* the master — via `static`, `dns`, or `gossip`
([discovery](../plans/phase-4-distributed.md)) — but there is **no automatic
failover**: if the master dies, the cluster has no leader until an operator
starts a new one.

This doc records what automatic failover would require. It is *not yet*
implemented — gossip discovery (Phase 4 slice 5) deliberately stops at
membership + leader lookup — but it is now **planned as its own effort**: see the
[leader failover plan](../plans/leader-failover.md) and the
[raft election decision](../decisions/raft-leader-election.md). The requirements
below are what that plan satisfies.

# Why discovery alone is not failover

Gossip discovery makes the master's address *findable* without static config,
and the `Discoverer` already re-resolves on every reconnect, so slaves would
follow a new leader automatically **once one exists**. The hard part failover
adds is *choosing* a new leader and *preserving the leader's state* — neither of
which discovery touches.

# What failover would require

1. **Leader election.** A protocol to elect exactly one leader and avoid
   split-brain (two masters). Options:
   - Election over the existing gossip ring (e.g. lowest-id-alive, or a bully
     algorithm) — simplest, but gossip membership is eventually-consistent, so
     it needs a tie-break/lease to avoid two nodes both believing they won.
   - Embed a consensus library (raft, e.g. `hashicorp/raft`) for a proper
     quorum-based leader + replicated log — robust, but a large dependency and a
     quorum (≥3 nodes) requirement.
   Quorum matters: a 2-node cluster cannot safely elect on partition.

2. **Ownership of master-only state.** Today several stores live only on the
   master, in memory or on its local disk:
   - the **project store** (`ProjectStore`) — projects, teams, assignments;
   - the **remote-context aggregation** (`remoteContexts`) — rebuilt from slave
     heartbeats, so largely self-healing;
   - the **AAP resume store** (`aap-resume.json`, per-node on disk).
   A newly-elected master starts empty. Failover needs either **replication** of
   this state (via the consensus log, or streamed to standbys) or a
   **shared/rebuildable store** (external DB, or reconstruct from slaves on
   promotion). The remote-context store already rebuilds from heartbeats; the
   project store and resume tokens are the state that would be lost.

3. **Re-targeting on leader change.** Slaves already re-resolve the leader each
   reconnect, so they follow a new master for free once elected. Clients/TUI
   enter at the master, so they need a stable entry point that survives failover
   — a virtual IP / DNS record / load balancer in front of the current leader,
   or client-side retry across known members (the gossip ring can supply the
   member list).

4. **Advertised address / entry point.** The elected leader must advertise a
   reachable `cluster.advertise_addr`; a VIP or updated DNS record is the usual
   way to keep one address pointing at whoever currently leads.

# Chosen shape

Build on the gossip ring: use it for membership + failure detection, layer
**raft** (`hashicorp/raft`) for single-leader safety, and make the project store
and resume tokens **replicated through the raft log** so an elected leader comes
up with current state. Keep the `Discoverer` abstraction — a `raftDiscoverer`
returning the *current* leader slots into the existing `leaderClient` re-resolve
path with no change to register/heartbeat. Failover is opt-in
(`cluster.failover: raft`, needs a ≥3-node quorum); the default static-master
path is unchanged.

This was chosen over a lease-based election over gossip and over warm-standby +
manual promotion — rationale in the
[raft election decision](../decisions/raft-leader-election.md); the sliced
implementation is the [leader failover plan](../plans/leader-failover.md). See
the [master/slave model](../decisions/master-slave-model.md) decision for the
topology this extends.
