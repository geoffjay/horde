//go:build integration

package server_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/geoffjay/horde/internal/api"
	"github.com/geoffjay/horde/internal/server"
)

// newAuthEnabledNode builds a started master with per-user auth enabled and
// the given users, fronted by the real API router on an httptest server. It
// is the integration counterpart to newAuthServer in the api package's unit
// tests: it exercises the full router → handler → server stack with a real
// configured user table.
func newAuthEnabledNode(t *testing.T, users ...server.UserAuth) (*server.Server, *httptest.Server) {
	t.Helper()
	if len(users) == 0 {
		users = []server.UserAuth{
			{ID: "alice", Token: "tok-a"},
			{ID: "bob", Token: "tok-b"},
		}
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
	ts := httptest.NewServer(api.Router(srv))
	t.Cleanup(ts.Close)
	return srv, ts
}

// authedDo issues a request with an Authorization: Bearer token against ts.
func authedDo(t *testing.T, ts *httptest.Server, method, path, token string, body string) (int, []byte) {
	t.Helper()
	var r *http.Request
	var err error
	if body != "" {
		r, err = http.NewRequest(method, ts.URL+path, strings.NewReader(body))
	} else {
		r, err = http.NewRequest(method, ts.URL+path, nil)
	}
	require.NoError(t, err)
	r.Header.Set("Content-Type", "application/json")
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(r)
	require.NoError(t, err)
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, b
}

// TestIntegration_AgentMutationGating asserts the four agent-mutation routes
// enforce requireUser end-to-end through the real router: an anonymous caller
// gets 401, an authenticated user reaches the handler (which then maps a bad
// input to a non-401 status), and a node caller (cluster token) passes. This
// is the integration counterpart to the unit tests in
// agent_mutation_authz_test.go; it catches router-wiring drift (a guard
// missing on a route) the unit tests cannot.
func TestIntegration_AgentMutationGating(t *testing.T) {
	srv, ts := newAuthEnabledNode(t)
	srv.SetClusterAuthTokenForTest("ct")

	// Anonymous → 401 on all four mutation routes.
	for _, tc := range []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{"create", http.MethodPost, "/api/v1/agents", `{"name":"greeter"}`},
		{"delete", http.MethodDelete, "/api/v1/agents/abc", ""},
		{"invoke", http.MethodPost, "/api/v1/agents/abc/invoke", `{"message":"hi"}`},
		{"approval", http.MethodPost, "/api/v1/agents/abc/approvals/r1", `{"decision":"allow"}`},
	} {
		t.Run("anon/"+tc.name, func(t *testing.T) {
			code, _ := authedDo(t, ts, tc.method, tc.path, "", tc.body)
			assert.Equal(t, http.StatusUnauthorized, code)
		})
	}

	// Authenticated user → reaches the handler. Each route's downstream
	// handler maps the bad input (unknown agent / bad approval) to a non-401
	// status, confirming the guard passed the request through.
	t.Run("user reaches handler", func(t *testing.T) {
		code, _ := authedDo(t, ts, http.MethodPost, "/api/v1/agents", "tok-a", `{}`)
		assert.Equal(t, http.StatusBadRequest, code, "create with empty body → 400, not 401")

		code, _ = authedDo(t, ts, http.MethodDelete, "/api/v1/agents/nope", "tok-a", "")
		assert.Equal(t, http.StatusNotFound, code, "delete unknown → 404, not 401")

		code, _ = authedDo(t, ts, http.MethodPost, "/api/v1/agents/nope/invoke", "tok-a", `{"message":"hi"}`)
		assert.Equal(t, http.StatusNotFound, code, "invoke unknown → 404, not 401")

		code, _ = authedDo(t, ts, http.MethodPost, "/api/v1/agents/nope/approvals/r1", "tok-a", `{"decision":"allow"}`)
		assert.Equal(t, http.StatusNotFound, code, "approval unknown → 404, not 401")
	})

	// Node principal (cluster token) → reaches the handler (cross-node
	// traffic is trusted at the edge).
	t.Run("node reaches handler", func(t *testing.T) {
		code, _ := authedDo(t, ts, http.MethodDelete, "/api/v1/agents/nope", "ct", "")
		assert.Equal(t, http.StatusNotFound, code, "node delete → 404, not 401")

		code, _ = authedDo(t, ts, http.MethodPost, "/api/v1/agents/nope/invoke", "ct", `{"message":"hi"}`)
		assert.Equal(t, http.StatusNotFound, code, "node invoke → 404, not 401")
	})
}

// TestIntegration_AgentMutationGating_DisabledRegression asserts the
// disabled-by-default behavior is byte-for-byte preserved: with no auth.users
// configured, an anonymous caller reaches each handler (same statuses as
// pre-3.5b slice 3). This is the regression guard the plan calls out — the
// existing suite must stay unchanged when auth is off.
func TestIntegration_AgentMutationGating_DisabledRegression(t *testing.T) {
	srv, err := server.New(server.Config{SpawnDefaultAgent: false})
	require.NoError(t, err)
	require.False(t, srv.AuthEnabled(), "auth disabled by default")
	require.NoError(t, srv.Start(context.Background()))
	t.Cleanup(func() {
		for _, a := range srv.Agents() {
			_ = srv.StopAgent(a.ID)
		}
	})
	ts := httptest.NewServer(api.Router(srv))
	defer ts.Close()

	// No auth.users ⇒ anonymous reaches each handler.
	code, _ := authedDo(t, ts, http.MethodPost, "/api/v1/agents", "", `{}`)
	assert.Equal(t, http.StatusBadRequest, code, "disabled: create bad body → 400")

	code, _ = authedDo(t, ts, http.MethodDelete, "/api/v1/agents/nope", "", "")
	assert.Equal(t, http.StatusNotFound, code, "disabled: delete unknown → 404")

	code, _ = authedDo(t, ts, http.MethodPost, "/api/v1/agents/nope/invoke", "", `{"message":"hi"}`)
	assert.Equal(t, http.StatusNotFound, code, "disabled: invoke unknown → 404")

	code, _ = authedDo(t, ts, http.MethodPost, "/api/v1/agents/nope/approvals/r1", "", `{"decision":"allow"}`)
	assert.Equal(t, http.StatusNotFound, code, "disabled: approval unknown → 404")
}

// TestIntegration_InvokeAuthz_MembershipEnforced spawns a real greeter bound to
// a project owned by alice, then asserts the invoke authorization (levelInvoke):
// the owner invokes OK, a non-member gets 403, and after being added to the
// team the member invokes OK. This exercises the full invoke wiring
// (requireUser → invokeAgent → authorizeProject(levelInvoke) → reverse proxy).
func TestIntegration_InvokeAuthz_MembershipEnforced(t *testing.T) {
	exe := findHordeBinary(t)

	srv, err := server.New(server.Config{
		AgentCommand:       exe,
		SocketDir:          "/tmp",
		ReadyTimeout:       10 * time.Second,
		HealthPollInterval: 0,
		SpawnDefaultAgent:  false,
		Users: []server.UserAuth{
			{ID: "alice", Token: "tok-a"},
			{ID: "bob", Token: "tok-b"},
		},
	})
	require.NoError(t, err)
	require.NoError(t, srv.Start(context.Background()))
	t.Cleanup(func() {
		for _, a := range srv.Agents() {
			_ = srv.StopAgent(a.ID)
		}
	})
	ts := httptest.NewServer(api.Router(srv))
	defer ts.Close()

	// Alice creates a project with a greeter — the agent is spawned and bound
	// to the project (so AgentActiveProject → this project). An explicit
	// workspace keeps the KB scaffold out of the source tree.
	createBody := `{"name":"p","workspace":"` + t.TempDir() + `","agents":["greeter"]}`
	code, body := authedDo(t, ts, http.MethodPost, "/api/v1/projects/", "tok-a", createBody)
	require.Equal(t, http.StatusCreated, code, "create project as alice: %s", body)
	var proj struct {
		ID   string `json:"id"`
		Team struct {
			Agents []struct {
				AgentID string `json:"agent_id"`
			} `json:"agents"`
		} `json:"team"`
	}
	require.NoError(t, json.Unmarshal(body, &proj))
	require.NotEmpty(t, proj.Team.Agents, "project has a team agent")
	agentID := proj.Team.Agents[0].AgentID
	require.NotEmpty(t, agentID)
	waitForAgentReady(t, srv, agentID)

	invoke := func(token string) int {
		code, _ := authedDo(t, ts, http.MethodPost, "/api/v1/agents/"+agentID+"/invoke", token, `{"message":"hi"}`)
		return code
	}

	// Owner → allowed.
	assert.Equal(t, http.StatusOK, invoke("tok-a"), "owner may invoke")
	// Non-member → 403.
	assert.Equal(t, http.StatusForbidden, invoke("tok-b"), "non-member is forbidden from invoking")
	// Add bob to the team → now allowed.
	code, body = authedDo(t, ts, http.MethodPost, "/api/v1/projects/"+proj.ID+"/users", "tok-a", `{"user_id":"bob"}`)
	require.Equal(t, http.StatusOK, code, "alice adds bob: %s", body)
	assert.Equal(t, http.StatusOK, invoke("tok-b"), "team member may invoke")
}

// TestIntegration_InvokeWithAuth_Succeeds spawns a real greeter agent through
// the API with an authenticated user token, invokes it, and asserts the SSE
// stream returns a token event. This validates the full invoke path
// (requireUser → invokeAgent → reverse proxy → agent subprocess) under auth.
func TestIntegration_InvokeWithAuth_Succeeds(t *testing.T) {
	exe := findHordeBinary(t)

	srv, err := server.New(server.Config{
		AgentCommand:       exe,
		SocketDir:          "/tmp",
		ReadyTimeout:       10 * time.Second,
		HealthPollInterval: 0,
		SpawnDefaultAgent:  false,
		Users:              []server.UserAuth{{ID: "alice", Token: "tok-a"}},
	})
	require.NoError(t, err)
	require.True(t, srv.AuthEnabled())
	require.NoError(t, srv.Start(context.Background()))
	t.Cleanup(func() {
		for _, a := range srv.Agents() {
			_ = srv.StopAgent(a.ID)
		}
	})
	ts := httptest.NewServer(api.Router(srv))
	defer ts.Close()

	// Spawn the greeter as an authenticated user.
	code, body := authedDo(t, ts, http.MethodPost, "/api/v1/agents", "tok-a", `{"name":"greeter"}`)
	require.Equal(t, http.StatusCreated, code, "spawn as user: %s", body)
	var agent map[string]any
	require.NoError(t, json.Unmarshal(body, &agent))
	agentID, _ := agent["id"].(string)
	require.NotEmpty(t, agentID)
	waitForAgentReady(t, srv, agentID)

	// Anonymous invoke → 401 (requireUser gate).
	code, _ = authedDo(t, ts, http.MethodPost, "/api/v1/agents/"+agentID+"/invoke", "", `{"message":"hi"}`)
	assert.Equal(t, http.StatusUnauthorized, code, "anonymous invoke rejected under auth")

	// Authenticated invoke → SSE stream with a token event.
	code, sse := authedDo(t, ts, http.MethodPost, "/api/v1/agents/"+agentID+"/invoke", "tok-a", `{"message":"hello"}`)
	require.Equal(t, http.StatusOK, code, "user invoke: %s", sse)
	text := extractSSEText(t, string(sse))
	assert.Contains(t, text, "hello", "authenticated invoke streams the agent's response")
}
