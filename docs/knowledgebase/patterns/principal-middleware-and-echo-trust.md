---
type: Pattern
title: Principal middleware + X-Horde-User echo-trust seam
description: A resolvePrincipal middleware labels every request anonymous/user/node (never rejects); requireUser + authorizeProject gate mutations; cross-node identity is echoed as X-Horde-User and honored only for a node principal. Opt-in — disabled is a true no-op.
tags: [pattern, auth, security, middleware, api, cluster]
timestamp: 2026-07-28T00:00:00Z
---

# Pattern

Per-user auth on the horde node API is built from three layers, each
independently composable and each a true no-op when `auth.users` is empty
(disabled-by-default). The same seam serves project mutations, agent
mutations, and the AAP invoke tool gate, and extends across the cluster via
an echo-trust header rather than a replicated user store.

See the decision: [Per-user API-token auth, ownership, and
permissions](../decisions/per-user-token-auth.md).

## 1. resolvePrincipal — label, never reject

`resolvePrincipal(srv)` is a global chi middleware (after `RequestID`,
before any route guard). It resolves only and **never rejects**: it reads
the `Authorization: Bearer` header, compares it constant-time against the
shared cluster token (⇒ `principalNode`) and then the `auth.users` table (⇒
`principalUser` with `userID`/`admin`/`scope`/`allowedTools`); otherwise
`principalAnonymous`. The stashed `principal` is read by downstream guards
and handlers via `principalFrom(r)`.

`requireClusterAuth` (the older node→node ingest guard) still rejects on the
three cluster routes; `resolvePrincipal` only annotates so handlers can
branch on identity later. When auth is disabled, the cluster-token path
still runs (so a node caller is labeled); an unrecognized token falls through
to anonymous.

**Why split resolve from reject:** the ingest routes need a hard reject
(`requireClusterAuth`), but the mutation routes need *attribution* (who is
the user) before they can authorize. One middleware that only labels keeps
the seam clean — a guard that rejects is a separate, opt-in layer.

## 2. requireUser + authorizeProject — the mutation gates

- `requireUser(srv)` — disabled ⇒ pass; node ⇒ pass (trusted cross-node
  traffic); user ⇒ pass; anonymous ⇒ 401. Runs **before** the
  project-forward middleware on a worker so an anonymous mutation is rejected
  at the edge, not forwarded to the coordinator as trusted node traffic. Never
  wraps health/ready or reads.
- `authorizeProject(srv, r, id, level{view|invoke|own})` — disabled ⇒ no-op
  (returns `nil, nil` so the handler's own existence check runs unchanged;
  authz never precedes the existence check when auth is off); admin ⇒ allow;
  `own` ⇒ `UserID == p.Owner`; `view`/`invoke` ⇒ owner OR `UserID ∈
  p.Team.Users`; else 403. A **node** principal is a worker→coordinator forward:
  the coordinator re-derives the echoed user (below) and enforces — it does
  **not** blanket-trust the node.

The two-level design (anonymous gate, then owner/team gate) keeps each guard
single-purpose: `requireUser` never needs the project, `authorizeProject`
never runs for an anonymous caller.

## 3. X-Horde-User — the cross-node echo-trust seam

A worker forwards project mutations and invoke reverse-proxies to the
coordinator/owning node, so the authoritative check runs where the
project/agent lives. The origin sets `X-Horde-User: <userID>` on the
forwarded request (already authenticated by the cluster token). The receiver
honors `X-Horde-User` **only when the caller is a node principal** (a valid
cluster token) and re-derives that user's scope/admin from local config via
`lookupForwardedUser`. Safe because an external client cannot forge it
without the cluster token. A node request without `X-Horde-User` is denied
on a mutation (no anonymous mutation via a node).

`resolveForwardedUser(r)` is the single read point: a user principal returns
its own id; a node principal honors the header; anonymous yields nothing.
`forwardedUser(r)` (the header value to echo) is the write point used by
the project-forward and invoke reverse-proxy paths.

## 4. The AAP tool gate — same seam, different enforcement point

The invoke path resolves a per-user `AAPUserScope` from the request
principal via `resolveAAPUserScope` (a user → its own `allowedTools`; a node
forward → re-derive from local config via `X-Horde-User`; anonymous/disabled
⇒ nil) and threads it through `AAPInvoke` → `runAAPTurn` → `sendPrompt`.
The host session's `resolveApproval` denies any tool not in the active
turn's `AllowedTools` (empty/nil ⇒ all tools allowed). This is the
enforcement point for the advisory per-user filesystem scope — the same
identity seam as the project gate, applied at AAP approval time.

# Mechanics

- `principal` is a small value stashed on the request context (`principalKey`);
  `principalFrom(r)` reads it (zero-value anonymous when none is stashed, so
  a direct test handler call works).
- Guards are plain `func(http.Handler) http.Handler` wrappers composed in
  `router.go` via `r.Group`/`r.Use`; handlers stay identity-unaware (the
  guard runs before them).
- `*server.Server` satisfies `authView`/`projectAuthorizer`/`invokeView`;
  tests use `fakeAuthView` (cluster token + small user table) for focused
  middleware tests without a real server.
- The user table is config-defined and identical on every node — no raft.
  Config drift across nodes is an operational concern: a forwarded unknown
  id ⇒ no restriction (not a block).

# Why

- **Disabled-by-default is a true no-op** — each layer returns/passes when
  `AuthEnabled()` is false, so a node with no `auth.users` is byte-for-byte
  backward compatible. The regression integration test is the guard.
- **One seam, three enforcement points** — project mutations, agent
  mutations, and the AAP tool gate all read the same resolved principal;
  cross-node identity uses the same echo-trust header.
- **No replicated user store** — config-defined users + an echoed header
  avoid a raft-replicated user table; the coordinator re-derives scope/admin
  from its own identical config.
- **The ADK session key is not unified with the principal** — `userID`
  stays `"local"` so team conversations stay shared (fracturing them would
  be a regression).

# See also

- [Per-user API-token auth, ownership, and permissions](../decisions/per-user-token-auth.md)
  — the full decision.
- [Project, team, and user model](../decisions/project-team-user-model.md) —
  the 3.5a/3.5b split.
- [Phase 3.5b plan](../plans/phase-3.5b-auth.md) — the five-slice
  implementation.
