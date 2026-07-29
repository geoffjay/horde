---
type: Decision
title: Per-user API-token auth, ownership, and permissions
description: Opt-in per-user API-token authentication on the node API; project ownership + owner/team authorization; the per-user AAP tool allowlist; the X-Horde-User cross-node echo-trust seam; no replicated user store; advisory-only filesystem scope at initialize.
tags: [decision, auth, security, users, projects, aap, phase-3.5b]
timestamp: 2026-07-28T00:00:00Z
---

# Context

Phase 3.5a built the project/team model and agent execution context with the
per-user half deliberately deferred: a project was owned by the node, the
`Team.Users`/`TeamUser` types were stubbed "reserved for 3.5b", the ADK runner
`userID` was hardcoded `"local"`, and the only principal seam was origin-based
(`local` vs `remote` for the execution-context redaction). The
[project/team/user decision](project-team-user-model.md) settled the split;
this decision records how the deferred 3.5b half landed.

See:

* [Phase 3.5b plan](/docs/knowledgebase/plans/phase-3.5b-auth.md) — the
  slice-by-slice implementation.
* [Project, team, and user model](project-team-user-model.md) — the 3.5a/3.5b
  split this completes.
* [HTTP + SSE transport](http-api-transport.md) and
  [cluster mTLS](/docs/knowledgebase/concepts/cluster-mtls.md) — the separate
  node→node transport-auth track (interim shared bearer token → mTLS).

# Decision

## Auth = per-user API tokens defined in config, opt-in

Per-user auth mirrors the existing `cluster.auth_token` mechanism: a user
presents `Authorization: Bearer <token>`, the node resolves it against an
`auth.users` config block (constant-time compare per entry, small N).
**Opt-in**: no `auth.users` ⇒ auth disabled, the API stays unauthenticated
byte-for-byte (current single-node behavior preserved). No login endpoint,
no password hashing at rest.

When auth is enabled, **mutations require a user token even from loopback**
(per-user attribution is the point). Reads stay open and origin-redacted as
3.5a; health/ready always open; node→node ingest unchanged (cluster token).
`admin: true` users bypass ownership checks (node-operator convenience).

## The principal seam: anonymous / user / node

`resolvePrincipal` is a global middleware that **labels** every request and
**never rejects** (the existing `requireClusterAuth` still gates the three
cluster ingest routes; `requireUser` is the mutation guard added later in
the chain). A request is resolved to one of:

* **node** — the bearer matches the shared cluster token (cross-node traffic).
* **user** — the bearer matches an `auth.users` entry (carrying scope, tools,
  admin).
* **anonymous** — no token, or auth disabled and no token.

`requireUser` (disabled ⇒ pass; node ⇒ pass; user ⇒ pass; anonymous ⇒ 401)
runs before project and agent mutation handlers; on a slave it runs **before**
the forward middleware so an anonymous mutation is rejected at the edge rather
than forwarded to the master as trusted node traffic. Project mutations
additionally call `authorizeProject` (owner + team members; see below).

## Authorization = owner + team members

`authorizeProject(srv, r, id, level{view|invoke|own})`:

* disabled ⇒ no-op (returns nil, nil — the handler's own existence check runs
  unchanged; authz never precedes the existence check when auth is off)
* node ⇒ a slave→master forward; enforce the echoed user (below), re-deriving
  admin from local config — do **not** blanket-trust the node
* admin ⇒ allow
* `own` ⇒ `UserID == p.Owner`
* `view`/`invoke` ⇒ owner OR `UserID ∈ p.Team.Users`
* else ⇒ 403

`own` gates lifecycle (pause/resume/finish), team membership, and agent
assignment; `invoke` gates the invoke path (owner OR team member may invoke
an agent bound to a project; standalone agents stay open to any
authenticated user). `view` is reserved (reads are open this phase).

## Cross-node identity: the X-Horde-User echo-trust seam

A slave forwards project mutations and invoke reverse-proxies to the
master/owning node, so the authoritative check runs where the project/agent
lives. The origin sets `X-Horde-User: <userID>` on forwarded requests
(already authenticated by the cluster token). The receiver honors
`X-Horde-User` **only when the caller is a node principal** (a valid cluster
token) and re-derives that user's scope/admin from local config. Safe
because an external client cannot forge it without the cluster token. A node
request without `X-Horde-User` is denied on a mutation (no anonymous mutation
via a node).

This also delivers per-user tool/scope to the owning node for invoke, which
need not traverse the master (a slave hosting the agent applies the tool
gate locally).

## No replicated user store

Users are config-defined and identical on every node, so the user table
needs no raft. Only the project `Owner`/`Team.Users` (already replicated)
change. A node with a user table missing a forwarded id treats it as "no
restriction" rather than blocking the turn — config drift is an operational
concern, not a runtime failure.

## Per-user AAP tool allowlist + advisory scope

Per-user tool restriction is enforced at AAP approval time
(`resolveApproval`'s tool gate): if the active turn's `AAPUserScope` has a
non-empty `AllowedTools` and the requested `tool_name` is not in it, the host
writes `DecisionDeny` immediately, regardless of `auto_approve`. Empty/nil
scope ⇒ all tools allowed (backward compatible). The scope is resolved by
the API layer from the request principal (a user → its own allowlist; a node
forward → re-derive from local config via `X-Horde-User`) and threaded
through `AAPInvoke` → `runAAPTurn` → `sendPrompt`; it is stashed under
`turnMu` and cleared in `endTurn` so it does not leak across turns.

**Advisory-only at initialize**: the AAP session is created at spawn, before
any user is known, and AAP v1 has no per-turn permission-update frame — so
per-user filesystem scope is enforced via the tool gate (a disallowed tool
can't touch the workspace), not via `initialize.permissions`.
`initialize.permissions` keeps the per-agent-def scope; `buildInitialize` is
the documented future hook (true per-user init scope would need per-user
agent instances — deferred).

## ADK runner userID stays "local"

The ADK conversation key is `(userID, sessionID=agent:project)`. Making
`userID` per-user would fracture the shared team conversation — the team
shares one conversation per agent per project. Authz identity is separate
from the ADK session key; do not unify them.

# Consequences

* Opt-in auth: a node with no `auth.users` is byte-for-byte backward
  compatible (no-op `requireUser`/`authorizeProject`/tool gate).
* A loopback caller needs a token for mutations once any user is configured
  (the TUI carries `HORDE_USER_TOKEN`/`--token`).
* `X-Horde-User` is security-critical: trusted iff the caller is a node
  principal; ignored otherwise. The disabled-by-default regression test is
  the guard that the open behavior is preserved.
* Node→node traffic must not require a user — the node principal bypasses
  `requireUser`; `requireClusterAuth` is unchanged.
* Per-user filesystem scope is advisory-via-tool-gate only (documented), not
  enforced at `initialize`. OS-level sandboxing remains deferred.
* The ADK session key is not unified with the principal — team conversations
  stay shared.
* The user table is not replicated; config drift across nodes is an
  operational concern (a forwarded unknown id ⇒ no restriction, not a
  block).

# See also

* [Phase 3.5b plan](/docs/knowledgebase/plans/phase-3.5b-auth.md) — the
  five-slice implementation (all complete).
* [Project, team, and user model](project-team-user-model.md) — the 3.5a
  half this decision fills in.
* [Principal middleware + echo-trust seam](/docs/knowledgebase/patterns/principal-middleware-and-echo-trust.md)
  — the recurring `resolvePrincipal`/`requireUser`/`authorizeProject` +
  `X-Horde-User` pattern, extracted as a pattern doc.
* [Cluster mTLS](/docs/knowledgebase/concepts/cluster-mtls.md) — the
  separate node→node transport-auth track.
