package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/geoffjay/horde/internal/server"
)

// fakeAuthView satisfies the authView interface for cluster-auth and
// principal-resolution tests. It carries a cluster token, an auth-enabled
// flag, and a small user table.
type fakeAuthView struct {
	clusterToken string
	enabled      bool
	users        []server.UserAuth
}

func (f fakeAuthView) ClusterAuthToken() string { return f.clusterToken }

func (f fakeAuthView) AuthEnabled() bool { return f.enabled }

func (f fakeAuthView) ResolveUser(token string) (server.UserAuth, bool) {
	for _, u := range f.users {
		if u.Token == token {
			return u, true
		}
	}
	return server.UserAuth{}, false
}

// Users returns the fake's user table with tokens stripped, mirroring the real
// server's read-only accessor.
func (f fakeAuthView) Users() []server.UserAuth {
	out := make([]server.UserAuth, 0, len(f.users))
	for _, u := range f.users {
		u.Token = ""
		out = append(out, u)
	}
	return out
}

func TestRequireClusterAuth(t *testing.T) {
	newReq := func(auth string) *http.Request {
		r := httptest.NewRequest(http.MethodPost, "/api/v1/cluster/register", nil)
		if auth != "" {
			r.Header.Set("Authorization", auth)
		}
		return r
	}
	run := func(token, auth string) (bool, int) {
		reached := false
		next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			reached = true
			w.WriteHeader(http.StatusOK)
		})
		rec := httptest.NewRecorder()
		requireClusterAuth(fakeAuthView{clusterToken: token})(next).ServeHTTP(rec, newReq(auth))
		return reached, rec.Code
	}

	// Configured token, correct bearer → pass.
	reached, code := run("s3cret", "Bearer s3cret")
	assert.True(t, reached)
	assert.Equal(t, http.StatusOK, code)

	// Configured token, wrong bearer → 401.
	reached, code = run("s3cret", "Bearer nope")
	assert.False(t, reached)
	assert.Equal(t, http.StatusUnauthorized, code)

	// Configured token, missing header → 401.
	reached, code = run("s3cret", "")
	assert.False(t, reached)
	assert.Equal(t, http.StatusUnauthorized, code)

	// No configured token → auth disabled, pass through even with no header.
	reached, code = run("", "")
	assert.True(t, reached)
	assert.Equal(t, http.StatusOK, code)
}
