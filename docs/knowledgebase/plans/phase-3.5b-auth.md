---
type: Plan
title: Phase 3.5b — Per-user auth, ownership, and permissions
description: Proposed plan for per-user API-token authentication, project ownership, owner+team authorization, and per-user tool/permission scopes on the node API; opt-in and backward-compatible, OS-level sandboxing deferred.
tags: [plan, auth, users, projects, teams, permissions, phase-3.5b, security]
timestamp: 2026-07-22T00:00:00Z
---

> **Status: complete — all five slices landed.** Identity plumbing (slice 1) +
> ownership + project authz (slice 2) + agent mutation gating (slice 3) +
> AAP tool allowlist + advisory scope (slice 4) + docs/KB (slice 5) are
> implemented. This plan completes the deferred half of the
> [project, team, and user model](../decisions/project-team-user-model.md) (the
> "3.5b" split) and lights up the TUI Users group (previously a placeholder in
> the [sidebar navigation](../patterns/tui-sidebar-navigation.md)).

# Context

horde's node API is **unauthenticated**. The project/team model (3.5a) was
deliberately shaped to defer per-user identity: a project is owned by the node,
`Team.Users`/`TeamUser` are stubbed "Reserved for 3.5b; empty in 3.5a", the ADK
runner `userID` is hardcoded `"local"`, and the only principal seam is
origin-based (`fullContextAllowed` = loopback-or-opt-in, `internal/api/context.go`).
The [roadmap](roadmap.md) lists four deferred 3.5b items: per-user auth, per-user
ownership + permission scopes, per-user tool restrictions, and OS-level
filesystem sandboxing.

This phase adds **per-user authentication, project ownership, and per-user
permissions** so the domain model gains its `owner` field and access control.

# Decisions

## Locked (resolved with the user)

1. **Auth = per-user API tokens defined in config**, mirroring
   `cluster.auth_token`. `Authorization: Bearer <token>` resolves to a user
   identity. **Opt-in**: no configured users ⇒ auth disabled, the API stays
   unauthenticated (current single-node behavior byte-for-byte preserved). No
   login endpoint, no password hashing at rest.
2. **Defer OS-level filesystem sandboxing** to a later follow-up. This phase:
   auth + ownership + tool allowlist + per-user permission scope merged into the
   existing **advisory** AAP permissions (no chroot/landlock/seatbelt, no
   `cmd.Dir`). See the [project/team/user decision](../decisions/project-team-user-model.md)
   §4 (advisory scope) and [cluster mTLS](../concepts/cluster-mtls.md) for the
   separate node→node transport track.
3. **Authorization = owner + team members.** Owner (creator) has full control
   (lifecycle, membership, delete); team members (`Team.Users`) may view +
   invoke; non-members denied when auth is enabled.

## Baked in during planning (open to change)

- **When auth is enabled, mutations require a user token even from loopback**
  (per-user attribution is the point). Reads stay open (as 3.5a); health/ready
  always open; node→node ingest unchanged (cluster token).
- **Reads are not gated** this phase — open + origin-redacted as 3.5a. Owner/member
  list-filtering of `GET /projects` is optional polish, deferred.
- **`admin: true`** users bypass ownership checks (node-operator convenience).
- **No replicated user store**: users are config-defined and identical on every
  node, so the user table needs no raft. Only the project `Owner`/`Team.Users`
  (already replicated) change.
- **ADK runner `userID` stays `"local"`** — it is the ADK conversation key
  `(userID, sessionID=agent:project)`; making it per-user would fracture the
  shared team conversation. Authz identity is separate from the ADK session key.

# 1. Config (`internal/config/horde.go`)

Add an `Auth AuthConfig` section (sibling of `Cluster`, keyed `auth`), modeled on
`ClusterConfig`:

```go
type AuthConfig struct { Users []UserDef `mapstructure:"users"` }
type UserDef struct {
    ID           string           `mapstructure:"id"`
    Token        string           `mapstructure:"token"`
    Admin        bool             `mapstructure:"admin"`
    AllowedTools []string         `mapstructure:"allowed_tools"` // empty = all
    Permissions  *PermissionScope `mapstructure:"permissions"`   // reuse existing type
}
```

Add `Config.AuthEnabled() bool` (`len(Auth.Users) > 0`) and `validateAuth()`
(separate method for gocyclo, like `validateCluster`): disabled ⇒ nil; else each
user needs non-empty `ID`+`Token`, reject duplicate IDs and duplicate tokens,
validate `Permissions.Mode` when set. Thread into `server.Config` via
`cmd/serve.go` with a `buildServerUsers` helper (mirror `buildServerAgentDefs`).

# 2. Principal + resolver middleware (`internal/api`)

- **Server side**: a `UserRegistry` built in `server.New` from
  `server.Config.Users []server.UserAuth`; expose via a new `authView`
  (`internal/api/types.go`): `AuthEnabled()`, `ResolveUser(token) (Principal,
  bool)` (constant-time compare per entry, small N), `ClusterAuthToken()`.
- **New `internal/api/principal.go`**: `principal{Kind(anon|user|node), UserID,
  Admin, Scope, AllowedTools}` + `withPrincipal`/`principalFrom` (context stash).
- **`resolvePrincipal(srv)` global middleware**, after `middleware.RequestID` in
  `Router`. Resolve only (never reject): bearer == cluster token ⇒ **node**; else
  `ResolveUser` match ⇒ **user** (with scope/tools/admin); else **anonymous**.
  Reuse `bearerToken` and `crypto/subtle`.
- `requireClusterAuth` stays as-is on the 3 ingest routes (it rejects;
  `resolvePrincipal` only labels).
- **`requireUser` guard** on mutation routes: disabled ⇒ pass; node ⇒ pass
  (trusted); user ⇒ pass to handler; anonymous ⇒ 401. Never wrap health/ready or
  reads.

# 3. Authorization: owner + team members

- `authorizeProject(p, principal, level{view|invoke|own}) error`: disabled/node/
  admin ⇒ allow; `own` ⇒ `UserID == p.Owner`; `invoke`/`view` ⇒ owner OR `UserID
  ∈ Team.Users`; else 403. Called in each project mutation handler.
- **Cross-node identity (chosen approach): echo `X-Horde-User` inside
  cluster-token-authenticated node→node traffic; enforce on the owning/coordinator
  node.** Project mutations forward worker→coordinator and invoke reverse-proxies to
  the owning node, so the authoritative check runs where the project/agent
  lives. The origin sets `X-Horde-User: <userID>` on forwarded requests
  (`projectForwardMiddleware`/`ForwardProjectRequest`; `invokeRemoteAgent`
  alongside `SetClusterAuth`). The receiver honors `X-Horde-User` **only when
  the caller is a node principal** (valid cluster token) and re-derives that
  user's scope from local config. Safe because an external client can't forge it
  without the cluster token. This also delivers per-user tool/scope to the owning
  node for invoke (which need not traverse the coordinator).

# 4. Ownership + team-membership (mechanical threading)

Add `Owner string` (`json:"owner,omitempty"` — absent ⇒ "", backward-compatible):
`server.Project`, `CreateProjectInput`, `createLocked`, `projectCommand`
(`internal/server/raftproject.go`), `raftProjectStore.Create`, `applyCommand`
opCreate, `projectDTO`+`toProjectDTO` (`internal/api/projects.go`),
`createProject` handler (`Owner = principalFrom(r).UserID`), `client.Project`.
Raft determinism preserved (owner is a deterministic string resolved on the
leader, like `Now`).

**Team membership write path is required** (nothing writes `Team.Users` today,
and config users can't self-join): add owner-only `POST /projects/{id}/users
{user_id}` + `DELETE /projects/{id}/users/{userID}`, `ProjectStore.AddUser/
RemoveUser` with `opAddUser`/`opRemoveUser` `projectCommand` ops replicated like
agents, and expose `Team.Users` in `teamDTO` (+ `client.ProjectTeam`).

# 5. Per-user tool allowlist + advisory scope (AAP)

The AAP session is created at spawn (user unknown); the user is known at invoke;
approvals resolve async in `readLoop`. Turns are serialized, so stash a per-turn
scope on the session:

- Add `turnUser *userScope` to `aapHostSession`, set under `turnMu` in
  `sendPrompt`, cleared in `endTurn` (`internal/server/aaphost.go`).
- Thread scope invoke→session: `invokeAAPAgent` reads `principalFrom(r)`;
  `Server.AAPInvoke` + `runAAPTurn` gain a scope arg passed to `sendPrompt`.
- **Tool gate in `resolveApproval`**: if `turnUser.AllowedTools` is non-empty and
  `req.ToolName` not in it ⇒ `respondApproval(id, DecisionDeny)` and return. Nil
  scope ⇒ current behavior.
- **Permission scope**: `buildInitialize` runs at spawn, before any user is
  known, and AAP v1 has no per-turn permission-update frame — so per-user
  filesystem scope is **advisory, best-effort via the tool gate** in 3.5b;
  `initialize.permissions` keeps the per-agent-def scope. `buildInitialize` is
  documented as the future hook. (True per-user init scope would need per-user
  agent instances — deferred.)

# 6. Client + TUI

- `internal/client/client.go`: add `authToken` + `SetAuth(token)`; set
  `Authorization: Bearer` in `send` and the streaming `Invoke` (audit all
  `http.NewRequestWithContext` sites).
- Token source: `HORDE_USER_TOKEN` env + a `--token` flag on `cmd/tui.go`, passed
  through `app.Run`→`app.New`→`c.SetAuth`.
- Populate `viewUsers`: new read endpoint `GET /api/v1/users` returning `[]{id,
  admin, you}` (**never tokens**); `client.Users()`; `renderUsersView` lists ids
  + admin badge + "(you)" + an auth enabled/disabled header line.

# Slices (each independently shippable + backward-compatible)

1. **Identity plumbing** — config `AuthConfig`+`validateAuth`, `UserRegistry`,
   `resolvePrincipal`, `X-Horde-User` echo/trust, `GET /users`, `Client.SetAuth`,
   TUI users view + token flag. No route rejects yet (disabled ⇒ no-op).
   **Done.**
2. **Ownership + project authz** — thread `Owner`, membership endpoints + store
   ops, `requireUser` + `authorizeProject` on project mutations. **Done.**
3. **Agent mutation gating** — `requireUser` on `POST /agents`, `DELETE
   /agents/{id}`, invoke, approvals; plus `authorizeProject` at `levelInvoke`
   on the invoke path (owner OR team member; non-member 403), enforced on the
   agent's owning node. **Done.**
4. **Tool allowlist + advisory scope** — thread `userScope` to the AAP session,
   gate in `resolveApproval`. **Done.**
5. **Docs/KB** + optional `invoked_by` attribution field (logging only, not the
   ADK session key). **Done** (the optional `invoked_by` field was not added —
   the invoke path has no logging today and the field was explicitly optional;
   the plan's primary deliverable, the docs/KB, is complete).

# Tests (respect the `//go:build integration` split)

- **Unit**: `validateAuth` table; `resolvePrincipal` (anon/user/node;
  `X-Horde-User` honored only for node); `authorizeProject` matrix
  (owner/member/stranger/admin/node/disabled × view/invoke/own); project handlers
  403/200; `UserRegistry.ResolveUser`; `Owner` in `createLocked` +
  `projectCommand` round-trip; membership ops; `resolveApproval` denies a
  disallowed tool (piped session with `turnUser`); `Client.SetAuth` header;
  `renderUsersView`.
- **Integration** (`//go:build integration`): auth-enabled node (unauth mutation
  → 401, owner → ok, member invoke → ok, stranger → 403); cross-node (worker
  forwards with `X-Horde-User`, coordinator enforces owner; invoke reverse-proxy
  carries user, tool allowlist denies on owning node); **disabled-by-default
  regression** (no `auth.users` ⇒ existing suite unchanged); node→node ingest
  still works with only the cluster token.

# KB / docs to update on landing

- **New decision** `decisions/per-user-token-auth.md`: token mechanism,
  opt-in/disabled parity with cluster auth, node-vs-user principal, the
  `X-Horde-User` echo-trust rule + why it's safe, no replicated user store, the
  advisory-scope-at-init limitation.
- **Update** `decisions/project-team-user-model.md` (fill the 3.5b half),
  `plans/roadmap.md` (move the three items from deferred to done; keep OS sandbox
  deferred), `docs/environment.md` + `concepts/environment.md`
  (`HORDE_USER_TOKEN`, `--token`, the `auth.users` block with an example),
  `patterns/index.md` (principal-middleware + echo-trust seam); `log.md` per
  slice.

# Risks / edge cases

- **Cross-node identity forgery** — `X-Horde-User` trusted *iff* caller is a node
  principal; ignored otherwise. Security-critical.
- **Disabled-by-default** must be a true no-op — the regression integration test
  is the guard.
- **Node→node traffic must not require a user** — node principal bypasses
  `requireUser`; `requireClusterAuth` unchanged.
- **AAP scope timing** — per-user filesystem scope is advisory-via-tool-gate only
  (documented), not enforced at `initialize`.
- **ADK session key** — do not unify `userID` with the principal (fractures team
  conversations).
- **Loopback + auth-enabled** requires a token for mutations — the TUI must carry
  `HORDE_USER_TOKEN` once any user is configured (surface 401/403 in the status
  line).

# Verification (when implemented)

- `task test` (unit, `-race`) + `task lint` + `task fmt`, then
  `task test:integration` for the cross-node/auth-enabled suites.
- Manual (dev node under overmind+air — use the overmind MCP tools, do not
  hand-start/kill horde): add an `auth.users` block, restart, and confirm
  against the API port: no token → reads ok but a project mutation → 401; owner
  token → create/pause ok; a second team user → can invoke but not pause (403);
  `GET /users` lists ids without tokens; the TUI Users view shows users with
  "(you)"; removing `auth.users` restores fully-open behavior.

# See also

* [Project, team, and user model](../decisions/project-team-user-model.md) — the
  3.5a/3.5b split this plan completes.
* [Roadmap](roadmap.md) — the deferred-to-3.5b items.
* [Cluster mTLS](../concepts/cluster-mtls.md) — the separate node→node transport
  auth track (interim shared bearer token → mTLS).
* [Agent execution context](agent-execution-context.md) — the origin-based
  principal seam this plan makes identity-aware.
