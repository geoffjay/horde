---
type: Decision
title: Raft for leader election and master-state replication
description: Adopt hashicorp/raft for quorum-based leader election and replicate master-only state through the raft log, rather than a lease over gossip or manual standby promotion.
tags: [decision, cluster, distributed, failover, raft, consensus]
timestamp: 2026-07-18T00:00:00Z
---

# Context

Through Phase 4 the master is **statically designated** (`horde serve --mode
master`) and is the single source of truth for cluster-wide state. Gossip
discovery (Phase 4 slice 5) makes the master's address *findable* without static
config and the `Discoverer` re-resolves each reconnect, so slaves already follow
a new leader **once one exists** — but nothing *elects* one, and nothing
preserves the master's state across the change. The
[cluster leader failover](../concepts/cluster-failover.md) concept doc records
what automatic failover requires: (1) single-leader election without split-brain,
(2) preservation of master-only state, (3) re-targeting on leader change, and
(4) a stable advertised entry point.

The two coupled decisions — *how to elect* and *how master state survives* — were
weighed together (raft vs. a lease over the gossip ring vs. warm-standby +
manual promote; replicate-via-election-layer vs. rebuild-on-promotion vs. an
external shared store).

# Decision

Adopt **`hashicorp/raft`** for leader election, and **replicate master-only
state through the raft log** (project store, AAP resume tokens) via a raft FSM.

* The raft cluster's leader *is* the horde master. A `raftDiscoverer` returns the
  current raft leader's HTTP address, slotting into the existing `leaderClient`
  re-resolve path with no change to register/heartbeat.
* Master-only mutations (project create/assign/state, resume-token capture)
  become raft log entries applied by an FSM, so a newly-elected leader comes up
  with current state instead of empty.
* The gossip ring is retained for *membership and failure detection*; raft is
  layered on top for *single-leader safety*. Gossip discovery stays as-is for
  the static-master deployments that do not enable failover.

# Why raft over the alternatives

* **vs. lease over gossip** (e.g. lowest-id-alive guarded by a renewable lease):
  lighter and works at 2 nodes, but gossip membership is eventually-consistent,
  so avoiding two nodes both believing they won needs a correct lease/fencing
  protocol — and it provides **no replicated log**, forcing a separate,
  hand-rolled state-replication story. Raft gives correct election *and* state
  replication from one well-tested primitive.
* **vs. warm-standby + manual promote:** smallest change, but not automatic — an
  operator (or external tooling + a VIP flip) is in the failure path. The goal
  here is *automatic* failover.
* **Replicate via the log vs. rebuild-on-promotion:** rebuild is least work and
  the remote-context store already self-heals from heartbeats, but the project
  store and resume tokens would be lost or need slaves to re-report. Since raft
  already ships a replicated log, applying master mutations through it is the
  natural, lossless choice — the state travels with the election.

# Consequences / accepted costs

* **A ≥3-node quorum is required** for safe failover. A 2-node cluster cannot
  safely elect on partition; failover is an **opt-in** mode (`cluster.failover:
  raft`), and the default single-master topology is unchanged. Deployments that
  do not enable it keep today's behaviour exactly.
* **A large new dependency** (`hashicorp/raft` + a log/stable store such as
  `raft-boltdb`). Justified by correctness — leader election and log replication
  are famously easy to get subtly wrong by hand.
* Master-only mutation paths must be routed through the FSM (an internal
  refactor of `ProjectStore` and `resumeStore` writes into raft `Apply` calls).
  This is the bulk of the implementation and is sliced in the
  [leader failover plan](../plans/leader-failover.md).
* A stable client/TUI **entry point** (VIP / DNS / client-side retry across
  known members) is still needed and is orthogonal to raft — the ring supplies
  the member list for client-side retry.

This extends the [master/slave cluster model](master-slave-model.md); see the
[leader failover plan](../plans/leader-failover.md) for the sliced
implementation and the [failover concept](../concepts/cluster-failover.md) for
the requirements this satisfies.
