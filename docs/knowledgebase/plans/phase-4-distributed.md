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

## Slice 2 — Agent placement / scheduling (complete)

The other half of "placement/coordination": **choose which node an agent
*spawns* on**. Cross-node invoke (slice 1) already routes to wherever an agent
lives, so a placed agent is immediately invokable through the master.

* **Placement request.** `POST /api/v1/agents` gains an optional `node` field:
  `""`/`"local"`/the local node id → spawn here (unchanged); a slave node id →
  place on that slave; `"auto"` → let the master choose.
* **Placement policy.** `Server.ResolveSpawnTarget(requested)` maps the request
  to a target. `"auto"` picks the least-loaded node among the master and its
  non-stale slaves (load = agent count; ties favour local, avoiding a network
  hop). An explicit slave id must be registered and non-stale, else
  `ErrNodeNotFound` (404). Remote placement is master-only
  (`ErrPlacementMasterOnly` on a slave) — direction is master→slave, mirroring
  slice 1 and the slave→master project forwarding.
* **Spawn forwarding.** For a remote target the master POSTs the spawn to the
  slave's own `/api/v1/agents` (`Server.ForwardSpawn`) carrying only the name
  (never a node, so it cannot loop) and relays the slave's response — including
  the id the slave assigned. The slave's next heartbeat (~5s) surfaces the
  agent in the aggregated view, at which point slice-1 invoke routing reaches
  it.

Verified end-to-end: on a two-node cluster, `POST /api/v1/agents` with
`node: "slave-1"` on the master spawned the agent on the slave and a subsequent
invoke through the master streamed its response back.

## Later slices (not started)

* **Discovery beyond `static`** — implement `cluster.discovery_mechanism`
  (dns/gossip) so nodes find peers without a hardcoded leader address.
* **Cross-node event fan-out** — the in-process `EventBus` is currently unused;
  wire it to real events, then propagate across nodes (HTTP/nng).

## Slice follow-ups (logged, out of scope)

* Client/TUI surface for the `node` placement field (slice 2 is API-level only,
  mirroring slice 1): a `--node` on the `horde` client and a node picker in the
  TUI new-agent flow.
* Node-qualified agent addressing (collision-proof) if bare-id resolution
  proves insufficient.
* Slave→master (and slave→slave) invoke forwarding for non-master entry points
  (mirrors the existing slave→master project forwarding).
* Register/heartbeat authentication.
