---
type: Plan
title: Leader failover
description: Automatic leader failover for a horde cluster — raft-based election on top of the gossip ring, master-only state replicated through the raft log, and a stable entry point that survives a leader change. Built in slices on the Phase 4 foundation.
tags: [plan, cluster, distributed, failover, raft]
---

Phase 4 makes a cluster *act* across nodes but keeps a **statically designated,
single-point-of-failure master**: if it dies, the cluster has no leader until an
operator starts a new one, and its in-memory/on-disk state is lost. This is the
next body of work — make leadership *survive* the loss of a node.

> **Status: complete.** All four slices have landed — raft election over the
> gossip ring (1), the project store replicated through the raft log (2), AAP
> resume tokens replicated (3), and a client that follows the leader across a
> failover (4). Failover is opt-in (`cluster.failover: raft`, needs a ≥3-node
> quorum); the default static-master path is unchanged.

Decision: [Raft for leader election and master-state replication](../decisions/raft-leader-election.md).
Requirements background: [cluster leader failover](../concepts/cluster-failover.md) concept.
Topology it extends: [master/slave cluster model](../decisions/master-slave-model.md).

## Principles

* **Opt-in, backwards-compatible.** Failover is a new mode
  (`cluster.failover: raft`, default `off`). With it off, horde behaves exactly
  as it does after Phase 4 — a static master, no quorum requirement, no new
  runtime cost. Nothing below changes the single-master default path.
* **Layer, don't replace.** Keep the gossip ring for membership + failure
  detection and keep the `Discoverer`/`leaderClient` re-resolve seam. Raft is
  layered on top; a `raftDiscoverer` returns the current leader and slots into
  the existing register/heartbeat path unchanged.
* **Slice independently.** Each slice lands on its own with the gate green
  (`task test` unit-only + deterministic; `task test:integration` for the
  multi-node behaviour), mirroring how Phase 4 was built. New heavy tests are
  `//go:build integration` per the
  [unit/integration split](../patterns/unit-integration-test-split.md).

## Slice 1 — Raft membership + election (leader lookup only) — complete

Stood up a raft cluster whose leader *is* the horde master, exposed through the
existing discovery seam — **no state replication yet** (the FSM is a no-op).
This proves election + re-targeting end to end before touching the stores.

Delivered: `internal/server/raft.go` (`raftNode` — `hashicorp/raft` +
`raft-boltdb/v2` log/stable store + file snapshots, TCP transport, gossip-driven
`addVoter`/`removeServer`, `bootstrapIfNeeded`) and `raftfsm.go` (a
handler-delegating FSM that no-ops until the stores are wired in). Config keys
`cluster.failover` / `raft_bind_addr` / `raft_advertise_addr` / `raft_dir`
(validated: `raft` needs `gossip` + a routable advertise addr). Role is dynamic
via `Server.isMaster()` (raft leader ⇒ master), threaded through the
master-gated methods, `Mode()`, `LeaderAddr()`, `LeaderConnected()`, and a new
`IsLeader()`. Gossip `nodeMeta` gained `raft_addr`; the `raftDiscoverer` maps the
raft leader id → its HTTP address via the ring. `startCluster` brings up raft
before gossip (so gossip advertises the raft addr); every failover node runs
`connectLeader` (which skips self-registration on the leader and re-registers on
demotion) and the leader runs `raftReconcileLoop` (AddVoter/RemoveServer from the
ring). Verified: `TestRaftFailover_ElectsSingleLeader` and
`TestRaftFailover_ReElectsOnLeaderCrash` (3-node in-process cluster: single
leader elected, followers register; kill the leader → survivors re-elect and the
remaining follower re-targets), plus `raftDiscoverer` unit tests and config
validation cases.

The original slice plan (unchanged) follows.

* **Config.** `cluster.failover` (`off` default | `raft`); `cluster.raft_bind_addr`
  / `cluster.raft_advertise_addr` (the raft transport addr, distinct from the
  gossip and HTTP addrs); `cluster.raft_dir` (under the XDG state dir) for the
  log/stable/snapshot stores. `Validate`: `failover: raft` requires a bootstrap
  path and rejects a cluster that cannot reach quorum (see bootstrap below).
* **Node.** A `raftNode` wrapper (`internal/server/raft.go`) around
  `hashicorp/raft` with a `raft-boltdb` log+stable store and a file snapshot
  store, mirroring the `gossipNode` shape (construct → bind transport → run →
  `shutdown()` on ctx cancel). A no-op FSM for this slice.
* **Bootstrap + membership.** First cluster brought up with
  `raft.BootstrapCluster`; subsequent nodes join as voters. Reuse the gossip ring
  as the membership source of truth: when a node appears/leaves the ring, the
  current raft leader `AddVoter`/`RemoveServer`s it, so operators configure
  gossip seeds only (no separate raft peer list). Guard against the 2-node
  quorum trap — refuse to enable failover below 3 configured voters (documented,
  validated where detectable).
* **Discovery.** `raftDiscoverer` (implements `Discoverer`) returns the current
  leader's **HTTP** address. Raft knows the leader's *raft* address; map it to
  the HTTP address via the gossip `nodeMeta` (already carries `api_addr`), or
  extend `nodeMeta` with the raft server id. `newDiscoverer` gains a `raft`
  branch alongside `static`/`dns`/`gossip`. Slaves re-resolve each reconnect, so
  they follow a new leader for free once elected.
* **Role from raft, not config.** Under `failover: raft` a node's master/slave
  role is *dynamic* — whoever holds raft leadership serves the master API; the
  others act as slaves. `--mode` becomes advisory (initial bootstrap hint) rather
  than a fixed assignment. Keep it fixed when `failover: off`.

Verify (`task test:integration`): a 3-node raft cluster elects one leader;
killing the leader elects a new one within the election timeout; a slave's
`raftDiscoverer` returns the new leader's HTTP addr and it re-registers there.

## Slice 2 — Replicate the project store through the log — complete

Made the project store (`ProjectStore` — projects, teams, assignments) a raft
FSM so a newly-elected leader has it.

Delivered: `raftProjectStore` (`internal/server/raftproject.go`) implements
`ProjectStore` — mutations encode a `projectCommand` and go through `raft.Apply`
(committed on a quorum, replayed by every replica's FSM); reads serve the locally
applied in-memory state. Determinism: the leader resolves the timestamp before
replicating and the FSM assigns the project id from the replicated `nextID`, so
every replica applies identically (verified). `memProjectStore` mutators were
split into `*Locked(…, now)` helpers reused by both the direct path and the FSM,
plus `snapshot()`/`restore()`. A top-level `raftCommand` envelope
(`raftstate.go`) dispatches by kind; the Server is the `raftFSMHandler`
(`applyCommand`/`snapshotState`/`restoreState`). Under `failover: raft`, `New`
builds the `raftProjectStore` (no `projects.json` — the log/snapshots are the
source of truth) and wires its apply to `Server.raftApply`. Follower forwarding
is unchanged: `projectForwardMiddleware` already routes mutations to the leader
(the raft leader under failover), so a follower never mutates locally. Verified:
`TestRaftFailover_ProjectStateSurvivesFailover` (create + pause on the leader,
crash it, new leader serves the same paused project) plus unit tests for the
CRUD-through-log path, deterministic cross-replica apply, invalid-input
short-circuit, and snapshot/restore.

The original slice plan (unchanged) follows.

* **FSM.** A `projectFSM` applies serialized mutations (create / update-state /
  assign / attach / remove / delete) to the in-memory project state; `Apply`
  replaces the direct `flush()`-to-`projects.json` write. Snapshots serialize the
  full state (the existing `persistedState` shape); `Restore` loads it.
* **Route mutations through raft.** Master-only project mutations
  (`internal/server/project_api.go`) become `raft.Apply(cmd)` on the leader.
  Non-leader nodes that receive a project mutation forward it to the leader
  (the existing slave→master project-forwarding path already does this; point it
  at the raft leader via the discoverer). Reads stay local to the leader's
  applied state.
* **Persistence.** Under `failover: raft` the raft log+snapshots are the source
  of truth; the standalone `projects.json` persistence path is bypassed (kept for
  `failover: off`). One store, two backends selected by mode.

Verify: mutate projects on the leader; kill it; the new leader serves the same
projects/teams/assignments. A mutation sent to a follower is forwarded and
applied. `failover: off` still uses `projects.json` unchanged.

## Slice 3 — Replicate AAP resume tokens — complete

Brought the per-node `resumeStore` (`aap-resume.json`, keyed by agent name) into
the replicated state so a respawn on a *new* leader still resumes the adapter
session.

Delivered: `resumeStore.set` replicates a `resumeCommand` through the raft log
(`raftKindResume`) when this node leads; the FSM applies it to every replica's
in-memory map. Under `failover: raft` the per-node `aap-resume.json` is bypassed
(the log/snapshots are the source of truth) and `New` wires
`resume.apply`/`resume.isLeader`. Snapshot/restore include the token map.

**Scope decision (documented):** resume state is replicated **cluster-global,
keyed by agent name** — node-agnostic on purpose, so a respawn on a *different*
newly-elected leader finds the token (keying by owning node would defeat failover
resume). A name collision across nodes shares the token, i.e. "same logical
agent". A resume-set that happens on a **follower** (an AAP agent placed off the
leader) falls back to a node-local write, matching pre-failover per-node
behavior — that is the boundary: leader-hosted agents' resume survives master
failover; follower-hosted agents keep node-local resume. Verified:
`TestRaftResume_ReplicatesAndSurvivesFailover` (white-box, 3 real raft nodes: a
token set on the leader replicates to followers and survives a leader crash) plus
unit tests (leader-replicates, non-leader-local-fallback, snapshot/restore).

The original slice plan (unchanged) follows.

* Fold resume-token capture (`turn_complete`) into the FSM as its own command
  type (or a second FSM under the same raft), replacing the per-node file write
  under `failover: raft`.
* Consider scope: resume tokens are today per-node and agents are per-node
  counters, so replication must key by a cluster-stable identity (agent name +
  owning node), not the bare per-node id. Decide whether resume state is truly
  cluster-global or only needs to survive *master* failover (project-hosting
  masters vs. slave-hosted agents). Document the boundary.

Verify: capture a resume token on the leader; fail over; a respawn resumes from
the replicated token.

## Slice 4 — Stable entry point + client retry — complete

Clients and the TUI enter at the master; after failover the address changes.
Gave them a stable way in.

Delivered: the `client.Client` now holds a set of member addresses
(`NewCluster`), and a unary request rotates to the next known member and retries
on a *transport* failure (an unreachable node — a crashed leader), so it follows
leadership across a failover with no external infrastructure. The member set is
seeded at construction and **learned** from `ListNodes` (every registered node's
address is merged), so a client that starts pointed at one node discovers the
rest and can fail over. HTTP status errors are not retried (only transport
failures). The TUI benefits automatically — it polls `ListNodes`, so its client
learns members and gains failover retry with no code change, and its cluster view
already surfaces the current leader. VIP/DNS remains the ops-managed alternative
(a single address in front of the current leader; the elected leader advertises a
reachable `cluster.advertise_addr`, already required under gossip). Verified:
`TestRaftFailover_ClientFollowsLeader` (a multi-member client keeps serving a
project across a leader crash) plus client unit tests (rotate-on-transport-error,
all-members-down, member learning/dedup from `ListNodes`).

The original slice plan (unchanged) follows.

* **Client-side retry across members.** The client learns the member set (from
  the gossip ring / a `GET /api/v1/cluster/nodes` seed list) and retries the
  current leader on connection failure, following the leader as it moves — no
  external infra required. This is the default.
* **Document the VIP/DNS option.** A virtual IP or an updated DNS record in
  front of the current leader is the ops-managed alternative; record it as a
  deployment note (the elected leader must advertise a reachable
  `cluster.advertise_addr`, already required under gossip).
* **TUI.** Surface leadership: which node is leader, and a reconnect that
  re-resolves rather than dying, extending the existing 60s-countdown reconnect
  in the [TUI-uses-node-API](../decisions/tui-uses-node-api.md) behaviour.

Verify: with the leader killed mid-session, the client/TUI reconnects to the new
leader and continues.

## Out of scope (log if they arise)

* **Replicating live agent processes.** Agents are subprocesses of the node that
  hosts them; failover preserves *cluster metadata* (projects, assignments,
  resume tokens), not running agent processes. A slave-hosted agent survives a
  master failover (the slave keeps running); a *master*-hosted agent dies with
  its node. Auto-rescheduling orphaned agents onto survivors is a later effort.
* **Learner/non-voter nodes** for read scaling or geo-distribution.
* **Dynamic quorum reconfiguration** beyond gossip-driven AddVoter/RemoveServer.
* **mTLS on the raft transport** — tracked with node→node auth in the
  [cluster mTLS](../concepts/cluster-mtls.md) concept; the interim
  `cluster.auth_token` does not cover the raft transport, so document that gap.

## Verification (whole effort)

* `task test` — unit-only, deterministic, no binary; `failover: off` paths
  unchanged.
* `task test:integration` — a raft multi-node suite (new
  `*_integration_test.go`): election, leader-kill re-election, state survival
  across failover (slices 2–3), follower-forwarding, and client re-target
  (slice 4). Docker: a `docker-compose.raft.yml` 3-node cluster as the
  full-topology check, mirroring `docker-compose.gossip.yml`.
* `task lint` — 0 in both default and `-tags=integration` modes.
* Docs each slice: `docs/environment.md` (new `cluster.*` keys), the
  [failover concept](../concepts/cluster-failover.md) (flip "not implemented" as
  slices land), the roadmap, and a `log.md` entry.
