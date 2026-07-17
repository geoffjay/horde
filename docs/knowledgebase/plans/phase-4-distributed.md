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

## Slice 3 — Discovery beyond `static` (complete)

Remove the hardcoded-leader-address dependency: a slave can **find its leader
via DNS** instead of a configured `server.leader`.

* **Mechanism.** `cluster.discovery_mechanism` is now honoured: `static`
  (default, dial the configured `server.leader`) or `dns` (an SRV lookup of
  `cluster.discovery_dns_name`). `gossip` remains a future option.
  `static`-with-a-hostname already resolves via the OS resolver, so the `dns`
  mechanism specifically adds **SRV** discovery — dynamic host+port and multiple
  prioritized targets, which a plain hostname cannot express.
* **Abstraction.** A `Discoverer` (`internal/server/discovery.go`) resolves the
  leader address: `staticDiscoverer` returns the configured address;
  `dnsDiscoverer` does an SRV lookup and picks the lowest-priority target (ties
  broken by highest weight), trimming the trailing dot and joining `host:port`.
  `newDiscoverer` returns `(nil, nil)` for a standalone slave (static, no
  leader), and an error for an unknown mechanism or a `dns` mechanism missing
  its name (also validated in `config.Validate`).
* **Re-resolution.** The `leaderClient` resolves through the `Discoverer` on
  every register/heartbeat/forward and caches the result for `leaderAddr()`, so
  a dns-discovered leader that moves or comes up later is picked up without a
  restart (a static discoverer seeds the cache immediately; dns resolves lazily
  in the background so `Start` never blocks on a lookup).

Verified: SRV target selection and the resolve→register path via an injected
resolver (`discovery_test.go`); config validation rejects `dns` with no name
and unknown mechanisms; the static path is unchanged (the real-API
register/heartbeat integration test still passes).

## Slice 4 — Cross-node event fan-out (complete)

Bring the dormant in-process `EventBus` to life as a **cluster-wide activity
feed**: a live stream of agent lifecycle transitions, aggregated at the master.

* **Real events.** The node now publishes agent lifecycle events on the bus at
  the natural seams: `agent.spawned` (after a subprocess starts and its context
  is initialized — ADK and AAP), `agent.exiting` (on `StopAgent`, before the
  process exits), and `agent.exited` (after the proc is reaped). Events carry an
  agent id, the origin node id, and the operator-chosen agent name only — no
  issue text, notes, or message content — so they are safe to propagate across
  nodes without the redaction the context store needs.
* **Local stream.** `GET /api/v1/events/stream` is an SSE feed of the bus
  (`streamEvents`). It carries only live events (no backlog replay), so the
  per-frame `id:` is for client correlation, not Last-Event-ID resume.
* **Cross-node fan-out (slave → master push).** Rather than a fan-in reverse
  proxy, events flow the same direction as heartbeat digests: a slave runs a
  `forwardEvents` goroutine that subscribes to its own bus and POSTs each event
  to the master's `POST /api/v1/cluster/events` (best-effort — a failed POST is
  logged and dropped). The master republishes received events onto its own bus
  (`PublishClusterEvent`), so the master's `/events/stream` is the whole
  cluster's feed with each event's origin node preserved. The receiver is
  master-only (a slave rejects it with 404), and the master never forwards, so
  there is no echo loop.

Verified: bus fan-out/drop-on-full/cancel and the republish path are unit
tested; the SSE framing and the master-only receiver are tested at the API
layer; the full `POST /cluster/events` → bus → `/events/stream` wiring is
covered through the router.

## Later slices (not started)

* **Gossip discovery** — a membership protocol (peer-to-peer), the other
  `discovery_mechanism` option beyond `static`/`dns`.

## Slice follow-ups (logged, out of scope)

* Client/TUI surface for the `node` placement field (slice 2 is API-level only,
  mirroring slice 1): a `--node` on the `horde` client and a node picker in the
  TUI new-agent flow.
* Node-qualified agent addressing (collision-proof) if bare-id resolution
  proves insufficient.
* Slave→master (and slave→slave) invoke forwarding for non-master entry points
  (mirrors the existing slave→master project forwarding).
* Register/heartbeat authentication.
