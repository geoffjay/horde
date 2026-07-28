package api

import (
	"net/http"
)

// userDTO is one entry in the GET /api/v1/users response. It surfaces only
// identity (id + admin badge + "(you)" marker); the token is never returned.
type userDTO struct {
	ID    string `json:"id"`
	Admin bool   `json:"admin"`
	You   bool   `json:"you"`
}

// usersResponse is the GET /api/v1/users response. AuthEnabled surfaces the
// node's auth mode so a client can render the users view header line.
type usersResponse struct {
	AuthEnabled bool      `json:"auth_enabled"`
	Users       []userDTO `json:"users"`
}

// listUsers returns the per-user identity list (ids + admin + you marker) and
// the node's auth-enabled state. Tokens are never surfaced. When auth is
// disabled (no auth.users) the response is an empty users list with
// auth_enabled=false, so a client renders a "disabled" header line.
func listUsers(srv authView) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// The users list is built from the server's auth.users registry; only
		// ids + admin badges. The resolved principal's user id is marked
		// "(you)" so the TUI can highlight the current identity.
		you := principalFrom(r).userID
		users := listServerUsers(srv, you)
		writeJSON(w, http.StatusOK, usersResponse{
			AuthEnabled: srv.AuthEnabled(),
			Users:       users,
		})
	}
}

// listServerUsers builds the userDTO list for a server's auth registry, marking
// the entry matching you as You. Split from listUsers so tests can call it
// directly against a fake authView. Returned in config declaration order;
// tokens are never included (authView.Users strips them).
func listServerUsers(srv authView, you string) []userDTO {
	if !srv.AuthEnabled() {
		return []userDTO{}
	}
	out := make([]userDTO, 0)
	for _, u := range srv.Users() {
		out = append(out, userDTO{
			ID:    u.ID,
			Admin: u.Admin,
			You:   u.ID == you,
		})
	}
	return out
}
