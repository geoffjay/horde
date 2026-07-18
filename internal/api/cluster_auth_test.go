package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

type fakeAuthView struct{ token string }

func (f fakeAuthView) ClusterAuthToken() string { return f.token }

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
		requireClusterAuth(fakeAuthView{token: token})(next).ServeHTTP(rec, newReq(auth))
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
