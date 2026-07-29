package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAgentMutation_AnonymousRejectedWhenAuthEnabled asserts the four
// agent-mutation routes wired through requireUser in the router reject an
// anonymous caller with 401 when per-user auth is enabled. The handlers
// themselves need not run — the guard returns before reaching them.
func TestAgentMutation_AnonymousRejectedWhenAuthEnabled(t *testing.T) {
	for _, tc := range []struct {
		name    string
		method  string
		target  string
		bodyNil bool
	}{
		{"create agent", http.MethodPost, "/api/v1/agents", false},
		{"delete agent", http.MethodDelete, "/api/v1/agents/abc", true},
		{"invoke agent", http.MethodPost, "/api/v1/agents/abc/invoke", false},
		{"respond approval", http.MethodPost, "/api/v1/agents/abc/approvals/r1", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := newAuthServer(t)
			h := Router(srv)

			var w *httptest.ResponseRecorder
			if tc.bodyNil {
				w = doWithAuth(t, h, tc.method, tc.target, "", nil)
			} else {
				w = doWithAuth(t, h, tc.method, tc.target, "", map[string]string{"name": "greeter"})
			}
			assert.Equal(t, http.StatusUnauthorized, w.Code, "anonymous mutation rejected when auth enabled")
		})
	}
}

// TestAgentMutation_UserPassesWhenAuthEnabled asserts a recognized user
// principal reaches the handler (the guard does not 401). Each route's
// downstream handler maps a missing/bad input to a non-401 status, confirming
// the guard passed the request through.
func TestAgentMutation_UserPassesWhenAuthEnabled(t *testing.T) {
	srv := newAuthServer(t)
	h := Router(srv)

	t.Run("create agent bad body", func(t *testing.T) {
		// Bad request body reaches createAgent's JSON decode → 400 (not 401).
		w := doWithAuth(t, h, http.MethodPost, "/api/v1/agents", "tok-a", createAgentRequest{})
		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("delete unknown agent", func(t *testing.T) {
		// Unknown agent id reaches deleteAgent → 404 (not 401).
		w := doWithAuth(t, h, http.MethodDelete, "/api/v1/agents/nope", "tok-a", nil)
		assert.Equal(t, http.StatusNotFound, w.Code)
	})

	t.Run("invoke unknown agent", func(t *testing.T) {
		// Unknown agent reaches invokeAgent's not-found branch → 404.
		w := doWithAuth(t, h, http.MethodPost, "/api/v1/agents/nope/invoke", "tok-a", map[string]string{"message": "hi"})
		assert.Equal(t, http.StatusNotFound, w.Code)
	})

	t.Run("respond approval unknown agent", func(t *testing.T) {
		// Unknown agent reaches respondApproval → 404 (not 401).
		w := doWithAuth(t, h, http.MethodPost, "/api/v1/agents/nope/approvals/r1", "tok-a", map[string]string{"decision": "allow"})
		assert.Equal(t, http.StatusNotFound, w.Code)
	})
}

// TestAgentMutation_NodePassesWhenAuthEnabled asserts a node principal
// (cluster-token bearer) passes requireUser on agent mutation routes, mirroring
// project mutations: cross-node traffic is trusted at the edge.
func TestAgentMutation_NodePassesWhenAuthEnabled(t *testing.T) {
	srv := newAuthServer(t)
	srv.SetClusterAuthTokenForTest("ct")
	h := Router(srv)

	t.Run("delete unknown agent", func(t *testing.T) {
		w := doForwarded(t, h, http.MethodDelete, "/api/v1/agents/nope", "ct", "")
		assert.Equal(t, http.StatusNotFound, w.Code, "node reaches handler (404, not 401)")
	})

	t.Run("invoke unknown agent", func(t *testing.T) {
		// doForwarded takes a nil body; invoke tolerates an empty body.
		w := doForwarded(t, h, http.MethodPost, "/api/v1/agents/nope/invoke", "ct", "")
		assert.Equal(t, http.StatusNotFound, w.Code, "node reaches handler (404, not 401)")
	})
}

// TestAgentMutation_DisabledPassesThrough asserts the existing
// auth-disabled behavior is byte-for-byte preserved: with no auth.users
// configured, an anonymous caller still reaches each handler (the same
// statuses as pre-3.5b slice 3).
func TestAgentMutation_DisabledPassesThrough(t *testing.T) {
	srv := newTestServer(t) // auth disabled
	h := Router(srv)

	t.Run("create agent bad body", func(t *testing.T) {
		w := do(t, h, http.MethodPost, "/api/v1/agents", createAgentRequest{})
		require.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("delete unknown agent", func(t *testing.T) {
		w := do(t, h, http.MethodDelete, "/api/v1/agents/nope", nil)
		assert.Equal(t, http.StatusNotFound, w.Code)
	})

	t.Run("invoke unknown agent", func(t *testing.T) {
		w := do(t, h, http.MethodPost, "/api/v1/agents/nope/invoke", map[string]string{"message": "hi"})
		assert.Equal(t, http.StatusNotFound, w.Code)
	})

	t.Run("respond approval unknown agent", func(t *testing.T) {
		w := do(t, h, http.MethodPost, "/api/v1/agents/nope/approvals/r1", map[string]string{"decision": "allow"})
		assert.Equal(t, http.StatusNotFound, w.Code)
	})
}
