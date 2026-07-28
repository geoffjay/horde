package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/geoffjay/horde/internal/server"
)

func TestResolvePrincipalRequest(t *testing.T) {
	alice := server.UserAuth{ID: "alice", Token: "tok-a"}
	bob := server.UserAuth{ID: "bob", Token: "tok-b", Admin: true, AllowedTools: []string{"read"}}

	newReq := func(auth, xUser string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/api/v1/users", nil)
		if auth != "" {
			r.Header.Set("Authorization", auth)
		}
		if xUser != "" {
			r.Header.Set(xHordeUserHeader, xUser)
		}
		return r
	}

	t.Run("anonymous when no token and auth disabled", func(t *testing.T) {
		srv := fakeAuthView{}
		p := resolvePrincipalRequest(srv, newReq("", ""))
		assert.Equal(t, principalAnonymous, p.kind)
	})

	t.Run("anonymous when no token and auth enabled", func(t *testing.T) {
		srv := fakeAuthView{enabled: true, users: []server.UserAuth{alice}}
		p := resolvePrincipalRequest(srv, newReq("", ""))
		assert.Equal(t, principalAnonymous, p.kind)
	})

	t.Run("user resolved when token matches", func(t *testing.T) {
		srv := fakeAuthView{enabled: true, users: []server.UserAuth{alice, bob}}
		p := resolvePrincipalRequest(srv, newReq("Bearer tok-b", ""))
		assert.Equal(t, principalUser, p.kind)
		assert.Equal(t, "bob", p.userID)
		assert.True(t, p.admin)
		assert.Equal(t, []string{"read"}, p.allowedTools)
	})

	t.Run("anonymous when token does not match", func(t *testing.T) {
		srv := fakeAuthView{enabled: true, users: []server.UserAuth{alice}}
		p := resolvePrincipalRequest(srv, newReq("Bearer nope", ""))
		assert.Equal(t, principalAnonymous, p.kind)
	})

	t.Run("node principal when cluster token matches", func(t *testing.T) {
		srv := fakeAuthView{clusterToken: "ct", enabled: true, users: []server.UserAuth{alice}}
		p := resolvePrincipalRequest(srv, newReq("Bearer ct", ""))
		assert.Equal(t, principalNode, p.kind)
	})

	t.Run("node wins over a user token equal to cluster token", func(t *testing.T) {
		// The cluster token is checked first; an auth.users entry sharing it
		// is shadowed and labels the caller as a node.
		srv := fakeAuthView{clusterToken: "shared", enabled: true, users: []server.UserAuth{{ID: "x", Token: "shared"}}}
		p := resolvePrincipalRequest(srv, newReq("Bearer shared", ""))
		assert.Equal(t, principalNode, p.kind)
	})
}

func TestResolveForwardedUser(t *testing.T) {
	newReq := func(p principal, xUser string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/api/v1/projects/", nil)
		if xUser != "" {
			r.Header.Set(xHordeUserHeader, xUser)
		}
		return r.WithContext(context.WithValue(r.Context(), principalKey{}, p))
	}

	t.Run("user principal returns its own id", func(t *testing.T) {
		r := newReq(principal{kind: principalUser, userID: "alice"}, "")
		uid, ok := resolveForwardedUser(r)
		assert.True(t, ok)
		assert.Equal(t, "alice", uid)
	})

	t.Run("node principal honors X-Horde-User", func(t *testing.T) {
		r := newReq(principal{kind: principalNode}, "bob")
		uid, ok := resolveForwardedUser(r)
		assert.True(t, ok)
		assert.Equal(t, "bob", uid)
	})

	t.Run("node principal without X-Horde-User yields empty", func(t *testing.T) {
		r := newReq(principal{kind: principalNode}, "")
		_, ok := resolveForwardedUser(r)
		assert.False(t, ok)
	})

	t.Run("anonymous yields empty", func(t *testing.T) {
		r := newReq(principal{kind: principalAnonymous}, "ignored")
		_, ok := resolveForwardedUser(r)
		assert.False(t, ok)
	})
}

// TestResolveForwardedUser_ThroughMiddleware exercises the security-critical
// echo-trust seam end to end: the principal is derived by the real
// resolvePrincipal middleware from the Authorization header, then a downstream
// handler calls resolveForwardedUser. An external caller cannot forge
// X-Horde-User because it is honored only for a node principal (cluster token).
func TestResolveForwardedUser_ThroughMiddleware(t *testing.T) {
	srv := fakeAuthView{
		clusterToken: "cluster-secret",
		enabled:      true,
		users:        []server.UserAuth{{ID: "alice", Token: "tok-a"}},
	}

	// Downstream handler reports the resolved forwarded user.
	handler := resolvePrincipal(srv)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		uid, ok := resolveForwardedUser(r)
		if !ok {
			w.Header().Set("X-Result", "-")
			return
		}
		w.Header().Set("X-Result", uid)
	}))

	run := func(auth, xUser string) string {
		r := httptest.NewRequest(http.MethodPost, "/api/v1/projects/", nil)
		if auth != "" {
			r.Header.Set("Authorization", auth)
		}
		if xUser != "" {
			r.Header.Set(xHordeUserHeader, xUser)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, r)
		return rec.Header().Get("X-Result")
	}

	t.Run("external user cannot forge X-Horde-User", func(t *testing.T) {
		// A real user token resolves to that user; the forged header is ignored.
		assert.Equal(t, "alice", run("Bearer tok-a", "victim"))
	})

	t.Run("anonymous caller with X-Horde-User is ignored", func(t *testing.T) {
		assert.Equal(t, "-", run("", "victim"))
	})

	t.Run("unrecognized token with X-Horde-User is ignored", func(t *testing.T) {
		assert.Equal(t, "-", run("Bearer nope", "victim"))
	})

	t.Run("node principal honors X-Horde-User", func(t *testing.T) {
		assert.Equal(t, "bob", run("Bearer cluster-secret", "bob"))
	})
}

func TestListUsersEndpoint(t *testing.T) {
	// Construct an auth-enabled server via Config.Users so Users()/AuthEnabled
	// are populated through the real construction path.
	authSrv, err := server.New(server.Config{
		SpawnDefaultAgent: false,
		Users: []server.UserAuth{
			{ID: "alice", Token: "tok-a"},
			{ID: "bob", Token: "tok-b", Admin: true},
		},
	})
	require.NoError(t, err)
	require.True(t, authSrv.AuthEnabled())

	t.Run("disabled returns empty list and auth_enabled=false", func(t *testing.T) {
		// newTestServer has no users by default, so AuthEnabled is false here.
		s := newTestServer(t)
		out := listServerUsers(s, "anyone")
		assert.Empty(t, out)
	})

	t.Run("enabled lists ids with admin and you marker", func(t *testing.T) {
		out := listServerUsers(authSrv, "alice")
		require.Len(t, out, 2)
		assert.Equal(t, "alice", out[0].ID)
		assert.False(t, out[0].Admin)
		assert.True(t, out[0].You)
		assert.Equal(t, "bob", out[1].ID)
		assert.True(t, out[1].Admin)
		assert.False(t, out[1].You)
	})

	t.Run("lists through the authView interface (fake)", func(t *testing.T) {
		// listServerUsers goes through authView.Users, so a fake resolves the
		// list without a concrete *server.Server.
		srv := fakeAuthView{enabled: true, users: []server.UserAuth{
			{ID: "alice", Token: "tok-a"},
			{ID: "bob", Token: "tok-b", Admin: true},
		}}
		out := listServerUsers(srv, "bob")
		require.Len(t, out, 2)
		assert.Equal(t, "alice", out[0].ID)
		assert.False(t, out[0].You)
		assert.Equal(t, "bob", out[1].ID)
		assert.True(t, out[1].Admin)
		assert.True(t, out[1].You)
	})
}
