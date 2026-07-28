package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/geoffjay/horde/internal/server"
)

// newAuthServer builds a started server with per-user auth enabled and the
// given users. The first user is the "owner" identity used in most tests.
func newAuthServer(t *testing.T, users ...server.UserAuth) *server.Server {
	t.Helper()
	if len(users) == 0 {
		users = []server.UserAuth{{ID: "alice", Token: "tok-a"}}
	}
	srv, err := server.New(server.Config{
		SpawnDefaultAgent: false,
		Users:             users,
	})
	require.NoError(t, err)
	require.True(t, srv.AuthEnabled())
	require.NoError(t, srv.Start(context.Background()))
	t.Cleanup(func() {
		for _, a := range srv.Agents() {
			_ = srv.StopAgent(a.ID)
		}
	})
	return srv
}

// doWithAuth is do() but sets an Authorization: Bearer header.
func doWithAuth(t *testing.T, h http.Handler, method, target, token string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body != nil {
		b, err := json.Marshal(body)
		require.NoError(t, err)
		r = httptest.NewRequest(method, target, bytes.NewReader(b))
		r.Header.Set("Content-Type", "application/json")
	} else {
		r = httptest.NewRequest(method, target, nil)
	}
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// createProjectAs creates a project directly via the store (bypassing agent
// spawn) as the given user, returning the created project. For authz tests we
// only need the project record (owner + team), not live agents.
func createProjectAs(t *testing.T, srv *server.Server, token, name string) *server.Project {
	t.Helper()
	owner := ""
	if token != "" {
		// Resolve the user id from the token via the server's registry.
		if u, ok := srv.ResolveUser(token); ok {
			owner = u.ID
		}
	}
	p, err := srv.CreateProjectForTest(server.CreateProjectInput{
		Name:       name,
		AgentNames: []string{"greeter"}, // recorded in the team; not spawned
		Owner:      owner,
	})
	require.NoError(t, err)
	return p
}

func TestCreateProject_SetsOwnerFromUser(t *testing.T) {
	srv := newAuthServer(t)
	p := createProjectAs(t, srv, "tok-a", "owned")
	assert.Equal(t, "alice", p.Owner)
}

func TestCreateProject_OwnerEmptyWhenAuthDisabled(t *testing.T) {
	srv := newTestServer(t)
	p := createProjectAs(t, srv, "", "noauth")
	assert.Empty(t, p.Owner, "auth disabled ⇒ owner is empty (backward compatible)")
}

func TestCreateProject_AnonymousRejectedWhenAuthEnabled(t *testing.T) {
	srv := newAuthServer(t)
	h := Router(srv)
	w := doWithAuth(t, h, http.MethodPost, "/api/v1/projects/", "", createProjectRequest{
		Name:       "x",
		AgentNames: []string{"greeter"},
	})
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestPauseProject_OwnerAllowed(t *testing.T) {
	srv := newAuthServer(t)
	h := Router(srv)
	p := createProjectAs(t, srv, "tok-a", "owned")
	w := doWithAuth(t, h, http.MethodPost, "/api/v1/projects/"+p.ID+"/pause", "tok-a", nil)
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestPauseProject_NonMemberForbidden(t *testing.T) {
	srv := newAuthServer(t, server.UserAuth{ID: "alice", Token: "tok-a"}, server.UserAuth{ID: "bob", Token: "tok-b"})
	h := Router(srv)
	p := createProjectAs(t, srv, "tok-a", "owned")
	w := doWithAuth(t, h, http.MethodPost, "/api/v1/projects/"+p.ID+"/pause", "tok-b", nil)
	assert.Equal(t, http.StatusForbidden, w.Code)
}

func TestPauseProject_MemberCanViewButNotPause(t *testing.T) {
	srv := newAuthServer(t, server.UserAuth{ID: "alice", Token: "tok-a"}, server.UserAuth{ID: "bob", Token: "tok-b"})
	h := Router(srv)
	p := createProjectAs(t, srv, "tok-a", "owned")
	// Alice adds Bob as a team member.
	w := doWithAuth(t, h, http.MethodPost, "/api/v1/projects/"+p.ID+"/users", "tok-a", addProjectUserRequest{UserID: "bob"})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	// Bob can read the project (view).
	w = doWithAuth(t, h, http.MethodGet, "/api/v1/projects/"+p.ID, "tok-b", nil)
	assert.Equal(t, http.StatusOK, w.Code)
	// Bob cannot pause (own-level).
	w = doWithAuth(t, h, http.MethodPost, "/api/v1/projects/"+p.ID+"/pause", "tok-b", nil)
	assert.Equal(t, http.StatusForbidden, w.Code)
}

func TestPauseProject_AdminBypassesOwnership(t *testing.T) {
	srv := newAuthServer(t, server.UserAuth{ID: "alice", Token: "tok-a"}, server.UserAuth{ID: "admin", Token: "tok-admin", Admin: true})
	h := Router(srv)
	p := createProjectAs(t, srv, "tok-a", "owned")
	w := doWithAuth(t, h, http.MethodPost, "/api/v1/projects/"+p.ID+"/pause", "tok-admin", nil)
	assert.Equal(t, http.StatusOK, w.Code)
}

// doForwarded simulates a slave→master forward: a node caller (cluster token)
// echoing X-Horde-User for the originating user.
func doForwarded(t *testing.T, h http.Handler, method, target, clusterToken, xUser string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, target, nil)
	r.Header.Set("Authorization", "Bearer "+clusterToken)
	if xUser != "" {
		r.Header.Set("X-Horde-User", xUser)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestPauseProject_ForwardedOwnerEnforcedOnMaster(t *testing.T) {
	// A forwarded (node) request is authorized against the echoed X-Horde-User,
	// not blanket-trusted: the master re-derives and enforces ownership.
	srv := newAuthServer(t, server.UserAuth{ID: "alice", Token: "tok-a"}, server.UserAuth{ID: "bob", Token: "tok-b"})
	srv.SetClusterAuthTokenForTest("ct")
	h := Router(srv)
	p := createProjectAs(t, srv, "tok-a", "owned")

	// Owner forwarded → allowed.
	w := doForwarded(t, h, http.MethodPost, "/api/v1/projects/"+p.ID+"/pause", "ct", "alice")
	assert.Equal(t, http.StatusOK, w.Code, "forwarded owner is enforced and allowed")

	// Non-owner forwarded → 403 (master enforces, does not blanket-trust node).
	w = doForwarded(t, h, http.MethodPost, "/api/v1/projects/"+p.ID+"/pause", "ct", "bob")
	assert.Equal(t, http.StatusForbidden, w.Code, "forwarded non-owner is forbidden")

	// Node with no forwarded user → 403 (no anonymous mutation via a node).
	w = doForwarded(t, h, http.MethodPost, "/api/v1/projects/"+p.ID+"/pause", "ct", "")
	assert.Equal(t, http.StatusForbidden, w.Code, "node without X-Horde-User is denied on a mutation")
}

func TestPauseProject_ForwardedAdminBypassesOwnership(t *testing.T) {
	// The master re-derives admin from local config for the forwarded user.
	srv := newAuthServer(t, server.UserAuth{ID: "alice", Token: "tok-a"}, server.UserAuth{ID: "admin", Token: "tok-admin", Admin: true})
	srv.SetClusterAuthTokenForTest("ct")
	h := Router(srv)
	p := createProjectAs(t, srv, "tok-a", "owned")

	w := doForwarded(t, h, http.MethodPost, "/api/v1/projects/"+p.ID+"/pause", "ct", "admin")
	assert.Equal(t, http.StatusOK, w.Code, "forwarded admin bypasses ownership")
}

func TestAddProjectUser_OwnerOnly(t *testing.T) {
	srv := newAuthServer(t, server.UserAuth{ID: "alice", Token: "tok-a"}, server.UserAuth{ID: "bob", Token: "tok-b"})
	h := Router(srv)
	p := createProjectAs(t, srv, "tok-a", "owned")

	// Bob (non-member, non-owner) cannot add a user.
	w := doWithAuth(t, h, http.MethodPost, "/api/v1/projects/"+p.ID+"/users", "tok-b", addProjectUserRequest{UserID: "bob"})
	assert.Equal(t, http.StatusForbidden, w.Code)

	// Alice (owner) adds Bob.
	w = doWithAuth(t, h, http.MethodPost, "/api/v1/projects/"+p.ID+"/users", "tok-a", addProjectUserRequest{UserID: "bob"})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var updated projectDTO
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &updated))
	require.Len(t, updated.Team.Users, 1)
	assert.Equal(t, "bob", updated.Team.Users[0].UserID)

	// Idempotent: adding Bob again is a no-op (still 200, still one entry).
	w = doWithAuth(t, h, http.MethodPost, "/api/v1/projects/"+p.ID+"/users", "tok-a", addProjectUserRequest{UserID: "bob"})
	require.Equal(t, http.StatusOK, w.Code)
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &updated))
	assert.Len(t, updated.Team.Users, 1)
}

func TestRemoveProjectUser_OwnerOnly(t *testing.T) {
	srv := newAuthServer(t, server.UserAuth{ID: "alice", Token: "tok-a"}, server.UserAuth{ID: "bob", Token: "tok-b"})
	h := Router(srv)
	p := createProjectAs(t, srv, "tok-a", "owned")
	// Add Bob.
	w := doWithAuth(t, h, http.MethodPost, "/api/v1/projects/"+p.ID+"/users", "tok-a", addProjectUserRequest{UserID: "bob"})
	require.Equal(t, http.StatusOK, w.Code)
	// Bob cannot remove himself (not owner).
	w = doWithAuth(t, h, http.MethodDelete, "/api/v1/projects/"+p.ID+"/users/bob", "tok-b", nil)
	assert.Equal(t, http.StatusForbidden, w.Code)
	// Alice removes Bob.
	w = doWithAuth(t, h, http.MethodDelete, "/api/v1/projects/"+p.ID+"/users/bob", "tok-a", nil)
	assert.Equal(t, http.StatusNoContent, w.Code)
	// Idempotent: removing again is 204 (no-op).
	w = doWithAuth(t, h, http.MethodDelete, "/api/v1/projects/"+p.ID+"/users/bob", "tok-a", nil)
	assert.Equal(t, http.StatusNoContent, w.Code)
}

func TestAddProjectUser_RequiresUserID(t *testing.T) {
	srv := newAuthServer(t)
	h := Router(srv)
	p := createProjectAs(t, srv, "tok-a", "owned")
	w := doWithAuth(t, h, http.MethodPost, "/api/v1/projects/"+p.ID+"/users", "tok-a", addProjectUserRequest{})
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestProjectDTO_SurfacesOwnerAndUsers(t *testing.T) {
	srv := newAuthServer(t, server.UserAuth{ID: "alice", Token: "tok-a"}, server.UserAuth{ID: "bob", Token: "tok-b"})
	h := Router(srv)
	p := createProjectAs(t, srv, "tok-a", "owned")
	_ = doWithAuth(t, h, http.MethodPost, "/api/v1/projects/"+p.ID+"/users", "tok-a", addProjectUserRequest{UserID: "bob"})

	w := doWithAuth(t, h, http.MethodGet, "/api/v1/projects/"+p.ID, "tok-a", nil)
	require.Equal(t, http.StatusOK, w.Code)
	var got projectDTO
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.Equal(t, "alice", got.Owner)
	require.Len(t, got.Team.Users, 1)
	assert.Equal(t, "bob", got.Team.Users[0].UserID)
}

func TestAuthorizeProject_DisabledIsNoOp(t *testing.T) {
	// Auth disabled: authorizeProject returns (nil, nil) — no project lookup.
	srv := newTestServer(t)
	r := httptest.NewRequest(http.MethodPost, "/api/v1/projects/x/pause", nil)
	p, err := authorizeProject(srv, r, "nonexistent", levelOwn)
	assert.Nil(t, err)
	assert.Nil(t, p, "disabled authz does not look up the project")
}

func TestRequireUser_DisabledPasses(t *testing.T) {
	srv := newTestServer(t) // auth disabled
	reached := false
	next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true })
	rec := httptest.NewRecorder()
	requireUser(srv)(next).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", nil))
	assert.True(t, reached, "disabled ⇒ pass through")
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestRequireUser_AnonRejectedWhenEnabled(t *testing.T) {
	srv := newAuthServer(t)
	reached := false
	next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true })
	rec := httptest.NewRecorder()
	requireUser(srv)(next).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", nil))
	assert.False(t, reached)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestRequireUser_UserPassesWhenEnabled(t *testing.T) {
	srv := newAuthServer(t)
	reached := false
	next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true })
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	r.Header.Set("Authorization", "Bearer tok-a")
	// resolvePrincipal stashes the principal on the context; requireUser reads
	// it. Chain them as the router does.
	resolvePrincipal(srv)(requireUser(srv)(next)).ServeHTTP(rec, r)
	assert.True(t, reached)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestRequireUser_NodePassesWhenEnabled(t *testing.T) {
	srv := newAuthServer(t)
	srv.SetClusterAuthTokenForTest("ct")
	reached := false
	next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true })
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	r.Header.Set("Authorization", "Bearer ct")
	resolvePrincipal(srv)(requireUser(srv)(next)).ServeHTTP(rec, r)
	assert.True(t, reached, "node principal passes requireUser")
}
