---
type: Plan
title: Phase 4 — Distributed
description: Making a horde cluster act across nodes — cross-node invoke routing, discovery, placement, and event fan-out — built in slices on the existing register/heartbeat/aggregation foundation.
tags: [plan, cluster, distributed, phase-4]
---

Phases 2–3.6 build a full single-node story and a cluster that *observes*
itself (slaves register, heartbeat, and the master aggregates remote agent
contexts — Phase 3.5a). Phase 4 makes the cluster *act* across nodes. It is
built in independent slices; each lands on its own.

## Slice 1 — Cross-node invoke + foundations (complete)

The first real distributed capability: **the master routes an invoke to
whichever node hosts the agent**, plus the two foundations it needs.

* **Advertised address.** `cluster.advertise_addr` (`HORDE_CLUSTER_ADVERTISE_ADDR`)
  is the routable `host:port` a node sends to the master on register;
  `localAddr()` uses it (falls back to `:<port>` with a warning). Fixes the
  former stub that stored an unroutable slave address — the prerequisite for
  any master→slave call.
* **Stale-slave eviction.** The master marks a slave `stale` after
  `slaveStaleAfter` (kept visible in the cluster view) and evicts it from the
  registry after `slaveEvictAfter`, bounding growth (`evictStaleSlavesLocked`).
* **Cross-node invoke.** `Server.RemoteAgentNode(agentID)` resolves a
  non-local agent id to its slave's address via the aggregated remote-context
  store (`nodeID/agentID`) + the slave registry, skipping stale/unknown nodes
  and refusing ambiguous ids (an id reported by >1 node — agent ids are
  per-node counters, so not globally unique). The invoke handler
  (`internal/api/invoke.go`) serves local agents as before; for a non-local id
  it reverse-proxies (`httputil.ReverseProxy`, streaming SSE) to
  `http://<addr>/api/v1/agents/{id}/invoke`. Direction is master→owning node.

Verified end-to-end: a two-node cluster (master + slave) where invoking the
slave's agent through the master streamed the agent's response back.

## Later slices (not started)

* **Discovery beyond `static`** — implement `cluster.discovery_mechanism`
  (dns/gossip) so nodes find peers without a hardcoded leader address.
* **Agent placement / scheduling** — choose a node to *spawn* an agent on
  (the other half of "placement/coordination"); cross-node invoke already
  routes to wherever an agent lives.
* **Cross-node event fan-out** — the in-process `EventBus` is currently unused;
  wire it to real events, then propagate across nodes (HTTP/nng).

## Slice 1 follow-ups (logged, out of scope)

* Node-qualified agent addressing (collision-proof) if bare-id resolution
  proves insufficient.
* Slave→master (and slave→slave) invoke forwarding for non-master entry points
  (mirrors the existing slave→master project forwarding).
* Register/heartbeat authentication.
