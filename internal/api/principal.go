package api

import (
	"context"
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/geoffjay/horde/internal/server"
)

// principalKind classifies a request caller.
type principalKind string

const (
	// principalAnonymous is the absence of a recognized identity (no token, or
	// auth disabled and no token).
	principalAnonymous principalKind = "anon"
	// principalUser is a recognized per-user identity resolved from a bearer
	// token in the auth.users config block.
	principalUser principalKind = "user"
	// principalNode is a peer node authenticated by the shared cluster token
	// (Authorization: Bearer <cluster.auth_token>).
	principalNode principalKind = "node"
)

// principal is the resolved caller identity stashed on the request context by
// resolvePrincipal. It carries the per-user scope (tools + advisory
// filesystem permissions) needed by authorization and the AAP tool gate.
// Slice 1 wires resolution + the context stash; route guards and authz land
// in later slices.
type principal struct {
	kind         principalKind
	userID       string
	admin        bool
	scope        *server.PermissionScope
	allowedTools []string
}

// principalKey is the context-stash key for the resolved principal.
type principalKey struct{}

// principalFrom returns the resolved principal from the request context, or a
// zero-value anonymous principal when none is stashed (a request that did not
// pass through resolvePrincipal — e.g. a test handler called directly).
func principalFrom(r *http.Request) principal {
	if p, ok := r.Context().Value(principalKey{}).(principal); ok {
		return p
	}
	return principal{kind: principalAnonymous}
}

// xHordeUserHeader is the cross-node identity echo. The origin sets it on a
// forwarded request (already authenticated by the cluster token); the receiver
// honors it only when the caller is a node principal (safe: an external client
// can't forge it without the cluster token).
const xHordeUserHeader = "X-Horde-User"

// resolvePrincipal labels each request with the resolved caller. It resolves
// only and never rejects: bearer == cluster token ⇒ node; else an
// auth.users match ⇒ user (with scope/tools/admin); else anonymous.
// requireClusterAuth still rejects on the three ingest routes; this middleware
// only annotates so handlers can branch on identity later.
//
// When auth is disabled (no auth.users) the bearer-token path is still
// checked against the cluster token so a node caller is labeled; an
// unrecognized token yields anonymous (a 3.5b-enabled node with auth.users
// will gate mutations in a later slice, but the principal is resolved here
// regardless).
func resolvePrincipal(srv authView) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p := resolvePrincipalRequest(srv, r)
			// Stash the server-readable principal adapter so KB scope resolvers
			// (internal/server) can authorize without importing internal/api.
			kbp := server.KBPrincipal{
				Kind:   server.KBPrincipalKind(p.kind),
				UserID: p.userID,
				Admin:  p.admin,
			}
			ctx := context.WithValue(r.Context(), principalKey{}, p)
			ctx = context.WithValue(ctx, server.KBPrincipalCtxKey{}, kbp)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// resolvePrincipalRequest resolves the principal for a single request. It is
// split from resolvePrincipal so tests can call it directly without spinning
// up a handler chain.
func resolvePrincipalRequest(srv authView, r *http.Request) principal {
	presented := bearerToken(r.Header.Get("Authorization"))

	// A node caller presents the shared cluster token. The match is
	// constant-time; empty token never matches a non-empty cluster token.
	if ct := srv.ClusterAuthToken(); ct != "" && subtle.ConstantTimeCompare([]byte(presented), []byte(ct)) == 1 {
		return principal{kind: principalNode}
	}

	// A user caller is resolved from the auth.users registry. When auth is
	// disabled ResolveUser is never called (no users), so an unrecognized
	// token falls through to anonymous.
	if srv.AuthEnabled() {
		if u, ok := srv.ResolveUser(presented); ok && u.Token != "" {
			return principal{
				kind:         principalUser,
				userID:       u.ID,
				admin:        u.Admin,
				scope:        u.Permissions,
				allowedTools: u.AllowedTools,
			}
		}
	}

	return principal{kind: principalAnonymous}
}

// resolveForwardedUser resolves the effective user identity for a request that
// arrived over a node→node forward. The receiver honors X-Horde-User only
// when the caller is a node principal (a request already authenticated by
// requireClusterAuth on the ingest route). On a direct/local request the
// stashed principal is used as-is. Returns the user id and true when a user
// identity applies; empty + false otherwise (anonymous or node).
func resolveForwardedUser(r *http.Request) (string, bool) {
	p := principalFrom(r)
	if p.kind == principalNode {
		if uid := strings.TrimSpace(r.Header.Get(xHordeUserHeader)); uid != "" {
			return uid, true
		}
		return "", false
	}
	if p.kind == principalUser {
		return p.userID, true
	}
	return "", false
}

// aapScopeResolver is the minimal surface resolveAAPUserScope needs: the
// auth-enabled flag and the user table (to re-derive a forwarded user's
// allowlist). Both authView and invokeView satisfy it.
type aapScopeResolver interface {
	AuthEnabled() bool
	Users() []server.UserAuth
}

// resolveAAPUserScope builds the per-user AAP turn scope from the request's
// principal, for the invoke tool gate. Returns nil when no per-user
// restriction applies:
//   - auth disabled ⇒ nil (no users configured — backward compatible)
//   - anonymous ⇒ nil (an anonymous caller has no allowlist; the agent-def
//     policy applies as before)
//   - user principal ⇒ the resolved user's allowlist (empty AllowedTools
//     ⇒ all tools allowed, returned as a non-nil scope with an empty list so
//     the gate's "empty ⇒ allow all" path runs)
//   - node principal (cross-node forward) ⇒ re-derive the allowlist from
//     local config via the echoed X-Horde-User; nil when the header is
//     absent (no forwarded user) or the id is not in local config
//     (forwarded from a node with a different user table — treated as no
//     restriction rather than blocking the turn).
//
// A non-nil scope with an empty AllowedTools means "all tools allowed" — the
// gate's "empty ⇒ allow all" path runs — so an admin or unrestricted user
// behaves identically to nil. The distinction matters only for attribution
// (the scope carries the UserID for logging).
func resolveAAPUserScope(srv aapScopeResolver, r *http.Request) *server.AAPUserScope {
	if !srv.AuthEnabled() {
		return nil
	}
	p := principalFrom(r)
	switch p.kind {
	case principalUser:
		// An empty AllowedTools on a user principal means "all tools allowed"
		// (the config default). Return a non-nil scope so the UserID is
		// available for attribution; the gate's empty-list path runs.
		return &server.AAPUserScope{
			AllowedTools: p.allowedTools,
			UserID:       p.userID,
		}
	case principalNode:
		uid, ok := resolveForwardedUser(r)
		if !ok {
			return nil
		}
		u, ok := lookupForwardedUser(srv, uid)
		if !ok {
			return nil
		}
		return &server.AAPUserScope{
			AllowedTools: u.AllowedTools,
			UserID:       uid,
		}
	}
	return nil
}
