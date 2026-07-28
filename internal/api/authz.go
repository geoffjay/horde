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
// otherwise a non-nil error the handler maps to a 403 (forbidden).
//
//   - disabled (no auth.users) ⇒ no-op (returns nil, nil — the handler's own
//     existence check + error mapping runs unchanged; authz never precedes
//     the existence check when auth is off)
//   - node principal ⇒ allow (cross-node traffic; the origin enforced)
//   - admin user ⇒ allow
//   - levelOwn ⇒ principal.UserID == project.Owner
//   - levelView/levelInvoke ⇒ owner OR principal.UserID ∈ project.Team.Users
//   - else ⇒ forbidden
//
// The project is looked up by id; an unknown project yields ErrProjectNotFound
// so the handler returns 404.
//
//nolint:unparam // level is always levelOwn in slice 2; slice 3 wires levelInvoke.
func authorizeProject(srv projectAuthView, r *http.Request, projectID string, level authLevel) (*server.Project, error) {
	// When auth is disabled, authorization is a no-op. Return nil (no project
	// lookup) so the handler's own existence check + error mapping runs
	// unchanged — the authz check never precedes the existence check in the
	// response when auth is off.
	if !srv.AuthEnabled() {
		//nolint:nilnil // intentional: no project, no error (disabled ⇒ no-op)
		return nil, nil
	}
	p, err := srv.GetProject(projectID)
	if err != nil {
		return nil, err
	}
	prin := principalFrom(r)
	switch prin.kind {
	case principalUser:
		return p, authorizeUser(p, prin.userID, prin.admin, level)
	case principalNode:
		// A node principal on a project mutation route is a slave→master
		// forward. Enforce the echoed user (X-Horde-User), re-deriving admin
		// from local config — do NOT blanket-trust the node. The origin slave
		// rejects anonymous mutations before forwarding (requireUser runs
		// before the forward middleware), so a forwarded mutation always
		// carries a user once auth is enabled; a node request without one is
		// denied.
		uid, ok := resolveForwardedUser(r)
		if !ok {
			return p, errForbidden
		}
		return p, authorizeUser(p, uid, forwardedUserIsAdmin(srv, uid), level)
	}
	return p, errForbidden
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

// forwardedUserIsAdmin reports whether the given user id is an admin per local
// config. resolveForwardedUser yields only the id, so the master re-derives
// admin from its own (identical, config-defined) user table.
func forwardedUserIsAdmin(srv authView, userID string) bool {
	for _, u := range srv.Users() {
		if u.ID == userID {
			return u.Admin
		}
	}
	return false
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
