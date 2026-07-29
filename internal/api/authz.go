package api

import (
	"errors"
	"net/http"

	"github.com/geoffjay/horde/internal/server"
)

// authLevel is the authorization level a project mutation requires.
type authLevel int

const (
	// levelView allows read access + invoke; the owner OR a team member.
	levelView authLevel = iota
	// levelInvoke allows agent invocation; the owner OR a team member.
	levelInvoke
	// levelOwn is the owner-only level (lifecycle, membership, delete).
	levelOwn
)

// errForbidden is the sentinel mapped to 403 by handlers.
var errForbidden = errors.New("forbidden")

// authorizeProject checks whether the resolved principal may perform an
// action on the project at the given level. Returns nil when allowed;
// otherwise a non-nil error the handler maps to a response (403 forbidden,
// 404 for an unknown project).
//
//   - disabled (no auth.users) ⇒ no-op (returns nil — the handler's own
//     existence check + error mapping runs unchanged; authz never precedes
//     the existence check when auth is off)
//   - admin user ⇒ allow
//   - levelOwn ⇒ principal.UserID == project.Owner
//   - levelView/levelInvoke ⇒ owner OR principal.UserID ∈ project.Team.Users
//   - else ⇒ forbidden
//
// The project is looked up by id; an unknown project yields ErrProjectNotFound
// so the handler returns 404. Project mutations call it at levelOwn; the
// invoke path calls it at levelInvoke (owner OR team member).
func authorizeProject(srv projectAuthorizer, r *http.Request, projectID string, level authLevel) error {
	// When auth is disabled, authorization is a no-op — no project lookup, so
	// the handler's own existence check + error mapping runs unchanged.
	if !srv.AuthEnabled() {
		return nil
	}
	p, err := srv.GetProject(projectID)
	if err != nil {
		return err
	}
	prin := principalFrom(r)
	switch prin.kind {
	case principalUser:
		return authorizeUser(p, prin.userID, prin.admin, level)
	case principalNode:
		// A node principal is a slave→master (or master→owning-node) forward.
		// Enforce the echoed user (X-Horde-User), re-deriving admin from local
		// config — do NOT blanket-trust the node. The origin slave rejects
		// anonymous mutations before forwarding (requireUser runs before the
		// forward middleware), so a forwarded request always carries a user
		// once auth is enabled; a node request without one is denied.
		uid, ok := resolveForwardedUser(r)
		if !ok {
			return errForbidden
		}
		return authorizeUser(p, uid, forwardedUserIsAdmin(srv, uid), level)
	}
	return errForbidden
}

// authorizeUser applies the owner/admin/team-membership rules for a concrete
// user id at the given level. Returns nil when allowed, errForbidden otherwise.
func authorizeUser(p *server.Project, userID string, admin bool, level authLevel) error {
	if admin || userID == p.Owner {
		return nil
	}
	if level == levelView || level == levelInvoke {
		for _, u := range p.Team.Users {
			if u.UserID == userID {
				return nil
			}
		}
	}
	return errForbidden
}

// userTable is the minimal surface for looking up a user by id in local
// config. Both projectAuthorizer (project authz) and aapScopeResolver (the
// AAP invoke tool gate) satisfy it; the lookup is shared.
type userTable interface {
	Users() []server.UserAuth
}

// lookupForwardedUser looks up a forwarded user id in local config, returning
// the matching UserAuth and true. resolveForwardedUser yields only the id, so
// the receiving node re-derives the user's admin flag + tool allowlist from
// its own (identical, config-defined) user table. Used by
// forwardedUserIsAdmin (project authz) and the invoke tool-gate scope (AAP).
func lookupForwardedUser(srv userTable, userID string) (server.UserAuth, bool) {
	for _, u := range srv.Users() {
		if u.ID == userID {
			return u, true
		}
	}
	return server.UserAuth{}, false
}

// forwardedUserIsAdmin reports whether the given user id is an admin per local
// config. resolveForwardedUser yields only the id, so the master re-derives
// admin from its own (identical, config-defined) user table.
func forwardedUserIsAdmin(srv userTable, userID string) bool {
	u, ok := lookupForwardedUser(srv, userID)
	return ok && u.Admin
}

// requireUser is the mutation-route guard. disabled ⇒ pass; node ⇒ pass
// (trusted cross-node traffic); user ⇒ pass to handler; anonymous ⇒ 401.
// Never wrap health/ready or reads.
func requireUser(srv authView) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !srv.AuthEnabled() {
				next.ServeHTTP(w, r)
				return
			}
			switch prin := principalFrom(r); prin.kind {
			case principalNode, principalUser:
				next.ServeHTTP(w, r)
			default:
				writeJSON(w, http.StatusUnauthorized, errorResponse{Error: "authentication required"})
			}
		})
	}
}

// resolveOwnerForCreate returns the user id to attribute a new project to.
// On a direct request it is the resolved user principal's id; on a
// forwarded (node) request it is the X-Horde-User value. Empty when auth is
// disabled (the project is unowned — backward compatible).
func resolveOwnerForCreate(srv authView, r *http.Request) string {
	if !srv.AuthEnabled() {
		return ""
	}
	if uid, ok := resolveForwardedUser(r); ok {
		return uid
	}
	return ""
}

// writeAuthzError maps an authorizeProject error to an HTTP response. A
// forbidden principal yields 403; an unknown project yields 404; anything
// else yields 500.
func writeAuthzError(w http.ResponseWriter, err error) {
	if errors.Is(err, errForbidden) {
		writeJSON(w, http.StatusForbidden, errorResponse{Error: "forbidden"})
		return
	}
	if errors.Is(err, server.ErrProjectNotFound) {
		writeJSON(w, http.StatusNotFound, errorResponse{Error: errProjectNotFound})
		return
	}
	writeJSON(w, http.StatusInternalServerError, errorResponse{Error: err.Error()})
}
