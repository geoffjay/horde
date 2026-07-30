---
type: Plan
title: Phase 4 — Distributed
description: Making a horde cluster act across nodes — cross-node invoke routing, discovery, placement, and event fan-out — built in slices on the existing register/heartbeat/aggregation foundation.
tags: [plan, cluster, distributed, phase-4]
---

Phases 2–3.6 build a full single-node story and a cluster that *observes*
itself (workers register, heartbeat, and the coordinator aggregates remote agent
contexts — Phase 3.5a). Phase 4 makes the cluster *act* across nodes. It is
built in independent slices; each lands on its own.

## Slice 1 — Cross-node invoke + foundations (complete)

The first real distributed capability: **the coordinator routes an invoke to
whichever node hosts the agent**, plus the two foundations it needs.

* **Advertised address.** `cluster.advertise_addr` (`HORDE_CLUSTER_ADVERTISE_ADDR`)
  is the routable `host:port` a node sends to the coordinator on register;
  `localAddr()` uses it (falls back to `:<port>` with a warning). Fixes the
  former stub that stored an unroutable worker address — the prerequisite for
  any coordinator→worker call.
* **Stale-worker eviction.** The coordinator marks a worker `stale` after
  `workerStaleAfter` (kept visible in the cluster view) and evicts it from the
  registry after `workerEvictAfter`, bounding growth (`evictStaleWorkersLocked`).
* **Cross-node invoke.** `Server.RemoteAgentNode(agentID)` resolves a
  non-local agent id to its worker's address via the aggregated remote-context
  store (`nodeID/agentID`) + the worker registry, skipping stale/unknown nodes
  and refusing ambiguous ids (an id reported by >1 node — agent ids are
  per-node counters, so not globally unique). The invoke handler
  (`internal/api/invoke.go`) serves local agents as before; for a non-local id
  it reverse-proxies (`httputil.ReverseProxy`, streaming SSE) to
  `http://<addr>/api/v1/agents/{id}/invoke`. Direction is coordinator→owning node.

Verified end-to-end: a two-node cluster (coordinator + worker) where invoking the
worker's agent through the coordinator streamed the agent's response back.

## Slice 2 — Agent placement / scheduling (complete)

The other half of "placement/coordination": **choose which node an agent
*spawns* on**. Cross-node invoke (slice 1) already routes to wherever an agent
lives, so a placed agent is immediately invokable through the coordinator.

* **Placement request.** `POST /api/v1/agents` gains an optional `node` field:
  `""`/`"local"`/the local node id → spawn here (unchanged); a worker node id →
  place on that worker; `"auto"` → let the coordinator choose.
* **Placement policy.** `Server.ResolveSpawnTarget(requested)` maps the request
  to a target. `"auto"` picks the least-loaded node among the coordinator and its
  non-stale workers (load = agent count; ties favour local, avoiding a network
  hop). An explicit worker id must be registered and non-stale, else
  `ErrNodeNotFound` (404). Remote placement is coordinator-only
  (`ErrPlacementCoordinatorOnly` on a worker) — direction is coordinator→worker, mirroring
  slice 1 and the worker→coordinator project forwarding.
* **Spawn forwarding.** For a remote target the coordinator POSTs the spawn to the
  worker's own `/api/v1/agents` (`Server.ForwardSpawn`) carrying only the name
  (never a node, so it cannot loop) and relays the worker's response — including
  the id the worker assigned. The worker's next heartbeat (~5s) surfaces the
  agent in the aggregated view, at which point slice-1 invoke routing reaches
  it.

Verified end-to-end: on a two-node cluster, `POST /api/v1/agents` with
`node: "worker-1"` on the coordinator spawned the agent on the worker and a subsequent
invoke through the coordinator streamed its response back.

## Slice 3 — Discovery beyond `static` (complete)

Remove the hardcoded-leader-address dependency: a worker can **find its leader
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
  `newDiscoverer` returns `(nil, nil)` for a standalone worker (static, no
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
feed**: a live stream of agent lifecycle transitions, aggregated at the coordinator.

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
* **Cross-node fan-out (worker → coordinator push).** Rather than a fan-in reverse
  proxy, events flow the same direction as heartbeat digests: a worker runs a
  `forwardEvents` goroutine that subscribes to its own bus and POSTs each event
  to the coordinator's `POST /api/v1/cluster/events` (best-effort — a failed POST is
  logged and dropped). The coordinator republishes received events onto its own bus
  (`PublishClusterEvent`), so the coordinator's `/events/stream` is the whole
  cluster's feed with each event's origin node preserved. The receiver is
  coordinator-only (a worker rejects it with 404), and the coordinator never forwards, so
  there is no echo loop.

Verified: bus fan-out/drop-on-full/cancel and the republish path are unit
tested; the SSE framing and the coordinator-only receiver are tested at the API
layer; the full `POST /cluster/events` → bus → `/events/stream` wiring is
covered through the router.

## Slice 5 — Gossip discovery (complete)

The last `discovery_mechanism`: a worker finds its coordinator through a **peer-to-peer
membership ring** — no per-worker leader address and no DNS.

* **Transport.** `github.com/hashicorp/memberlist` (SWIM) — the de-facto Go
  gossip library, chosen over a hand-rolled protocol for its mature failure
  detection and anti-entropy.
* **Discovery only.** The coordinator is still statically designated
  (`--mode coordinator`); there is no leader election. Every node joins the ring; the
  coordinator advertises `role=coordinator` plus its HTTP address in memberlist node
  metadata (`nodeMeta`, JSON, ≤512 B — a role and an address, nothing
  sensitive). A worker's `gossipDiscoverer` reads the ring for the coordinator's
  address and then registers/heartbeats over HTTP exactly as before — the
  `leaderClient` re-resolves through the `Discoverer` each reconnect, so gossip
  slots in with no change to the register path.
* **Node.** `internal/server/gossip.go` wraps memberlist: `newGossipNode` binds
  the listeners (fatal on failure, surfaced from `Start`), does an initial
  `Join(seeds)`, and runs a background re-join loop while no coordinator is visible
  so a node that starts before the coordinator converges without a restart.
  `leaderAPIAddr()` scans members for `role=coordinator`; `shutdown()` (Leave +
  Shutdown) runs on `ctx` cancel (there is no `Stop()`). A `gossipMembers` seam
  keeps `gossipDiscoverer` unit-testable without binding ports (mirrors the
  `lookupSRV` seam for dns).
* **Config.** `cluster.gossip_bind_addr`, `cluster.gossip_advertise_addr`, and
  `cluster.gossip_seeds` (a comma-separated **string**, not a list, so it also
  works via `HORDE_CLUSTER_GOSSIP_SEEDS` — viper here has no slice-env decode
  hook). `Validate` requires seeds for a gossip *worker* (a coordinator is the seed).
  Under `gossip` the coordinator must set `cluster.advertise_addr` so the HTTP
  address it gossips is routable.

Verified: real two-node memberlist convergence on loopback
(`gossip_test.go`), the discoverer via a fake, config validation, and
end-to-end on a three-node docker cluster (`docker-compose.gossip.yml`) where
workers registered with the coordinator having discovered its address through gossip
alone (no `server.leader`), then a worker-hosted agent was invoked through the
coordinator.

**Leader failover is out of scope** — see the
[cluster leader failover](../concepts/cluster-failover.md) concept doc for what
it would require.

## Phase 4 complete

All five slices have landed: cross-node invoke routing (1), agent placement (2),
dns discovery (3), cross-node event fan-out (4), and gossip discovery (5). A
horde cluster now acts across nodes — routing, placement, discovery, and a
cluster-wide event feed. Automatic leader failover is deliberately deferred
(concept doc above).

## Slice follow-ups (logged, out of scope)

* Client/TUI surface for the `node` placement field (slice 2 is API-level only,
  mirroring slice 1): a `--node` on the `horde` client and a node picker in the
  TUI new-agent flow.
* Node-qualified agent addressing (collision-proof) if bare-id resolution
  proves insufficient.
* Worker→coordinator (and worker→worker) invoke forwarding for non-coordinator entry points
  (mirrors the existing worker→coordinator project forwarding).
* Register/heartbeat authentication.
