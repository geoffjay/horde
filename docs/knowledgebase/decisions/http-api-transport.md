---
type: Decision
title: HTTP + SSE for the node API transport
description: The node API uses HTTP/JSON with SSE for streaming; no brokered pub/sub in Phase 2.
tags: [decision, api, transport, networking]
timestamp: 2026-07-09T00:00:00Z
---

# Context

Phase 2 needs a transport for the node API, which serves two channels:

* **TUI ↔ server** — agent control and event streaming.
* **slave ↔ master** — health, registration, heartbeat.

See the [Phase 2 plan](/docs/knowledgebase/plans/phase-2-server-api.md) for the
full surface. The transport choice drives every downstream handler and client,
so it has to be settled before any of that is built.

Three options were considered:

## gRPC

Schema control via protobuf is a strong benefit, and bidirectional streaming is
first-class. But gRPC imposes a higher requirement on consuming services and
clients: a generated stub per language, HTTP/2, and calls from a terminal are
harder than plain HTTP. For a node API that we want to be curl-able and trivial
to consume from the TUI, that friction is not worth the schema control alone.

## JSON-RPC

Lighter than gRPC and still schema-ish, but similar to gRPC it makes direct
terminal interaction harder without a real ergonomics win over HTTP/JSON. No
performance benefit over HTTP/JSON for horde's request volume. Rejected.

## HTTP/JSON

Curl-able, low client burden, broad tooling, and Go has many good server
options. Content-type negotiation leaves room for other encodings later if
needed. The one gap — streaming — is covered by SSE, which also gives us
`Last-Event-ID` resume for free: valuable for long agent token streams that get
interrupted. The one thing SSE cannot do is client→server streaming on the same
connection; for horde that is fine, since agent invocation is a normal POST and
the *response* is the SSE stream. True bidirectional would require WebSocket,
which horde does not need.

# Decision

**HTTP/JSON for request/response, SSE for server→client streaming.** Use the
stdlib (`net/http`, Go 1.22+ pattern routing) unless middleware composition
later justifies a thin router like `chi`. API versioning under `/api/v1`.

## Pub/sub: in-process event bus, no broker

Agent events flow over an **in-process event bus** (Go channels) exposed to
clients via SSE. The server owns the bus; multiple in-process consumers (an SSE
response handler, a slave forwarding events upstream) fan out trivially via
subscriptions, and clients never need a broker library.

Brokerless messaging libraries (ZeroMQ, nng/nanomsg) were considered for the
pub/sub layer and **deferred**. They earn their complexity only when publisher
and subscriber are separate processes with no shared hub. In horde the server
*is* the hub, so an in-process bus is strictly simpler and still brokerless.
Cross-node event fan-out is a Phase 4 problem (see the
[roadmap](/docs/knowledgebase/plans/roadmap.md)); if HTTP fan-out proves
insufficient then, nng (nanomsg-next-generation, the actively-maintained
brokerless option) is the candidate. ActiveMQ and other brokered options are
out of scope by design.

> **Naming note.** The brokerless ZeroMQ-alternative lineage is
> `nanomsg → nng` (nanomsg-next-generation). "NanoMQ" is EMQ's edge *MQTT
> broker* and is brokered — it is not the successor to ZeroMQ.

# Consequences

* One transport for both channels (TUI ↔ server and slave ↔ master); the master
  is just another node, so a slave reuses the same client code to talk to it.
* `server.port`, `server.leader`, and the read/write/idle timeouts already in
  `ServerConfig` (currently unused) are consumed by the new HTTP listener.
* SSE gives `Last-Event-ID` resume for agent token streams; gRPC streaming has
  no equivalent without custom checkpointing.
* The event bus is internal (not an endpoint); SSE handlers subscribe to it
  filtered by invocation id. This is the seam Phase 3 (real agents driven by
  the API) plugs into — the bus + SSE shape does not change, only what the
  subprocess emits.
* A second transport (nng or similar) is *not* locked in; the cross-node event
  shape can be decided in Phase 4 with the distributed topology better
  understood.
