//go:build integration

package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/geoffjay/horde/internal/api"
	"github.com/geoffjay/horde/internal/server"
)

// projectAPIClient is a thin helper for the project API over an httptest
// server. It keeps the integration tests readable without pulling in the
// internal package types.
type projectAPIClient struct {
	t  *testing.T
	ts *httptest.Server
}

func newProjectAPIClient(t *testing.T, ts *httptest.Server) *projectAPIClient {
	return &projectAPIClient{t: t, ts: ts}
}

func (c *projectAPIClient) do(method, path string, body any) (int, []byte) {
	c.t.Helper()
	var r *http.Request
	if body != nil {
		data, err := json.Marshal(body)
		require.NoError(c.t, err)
		r = httptest.NewRequest(method, path, bytes.NewReader(data))
		r.Header.Set("Content-Type", "application/json")
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	w := httptest.NewRecorder()
	c.ts.Config.Handler.ServeHTTP(w, r)
	return w.Code, w.Body.Bytes()
}

func (c *projectAPIClient) create(name string, agents []string) (int, map[string]any) {
	workspace := c.t.TempDir()
	code, body := c.do(http.MethodPost, "/api/v1/projects/", map[string]any{
		"name":      name,
		"workspace": workspace,
		"goal":      "test goal",
		"agents":    agents,
	})
	var resp map[string]any
	if len(body) > 0 {
		require.NoError(c.t, json.Unmarshal(body, &resp))
	}
	return code, resp
}

func (c *projectAPIClient) get(id string) (int, map[string]any) {
	code, body := c.do(http.MethodGet, "/api/v1/projects/"+id, nil)
	var resp map[string]any
	if len(body) > 0 {
		require.NoError(c.t, json.Unmarshal(body, &resp))
	}
	return code, resp
}

func (c *projectAPIClient) list() (int, []map[string]any) {
	code, body := c.do(http.MethodGet, "/api/v1/projects/", nil)
	var resp []map[string]any
	if len(body) > 0 {
		require.NoError(c.t, json.Unmarshal(body, &resp))
	}
	return code, resp
}

func (c *projectAPIClient) postAction(id, action string) (int, map[string]any) {
	code, body := c.do(http.MethodPost, "/api/v1/projects/"+id+"/"+action, nil)
	var resp map[string]any
	if len(body) > 0 {
		require.NoError(c.t, json.Unmarshal(body, &resp))
	}
	return code, resp
}

func (c *projectAPIClient) assignAgent(id, agentName string) (int, map[string]any) {
	code, body := c.do(http.MethodPost, "/api/v1/projects/"+id+"/agents", map[string]any{
		"name": agentName,
	})
	var resp map[string]any
	if len(body) > 0 {
		require.NoError(c.t, json.Unmarshal(body, &resp))
	}
	return code, resp
}

func (c *projectAPIClient) invoke(agentID, message string) (int, string) {
	c.t.Helper()
	resp, err := http.Post(
		c.ts.URL+"/api/v1/agents/"+agentID+"/invoke",
		"application/json",
		strings.NewReader(`{"message":"`+message+`"}`),
	)
	require.NoError(c.t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(c.t, err)
	return resp.StatusCode, string(body)
}

// extractSSEText extracts the text content from the first "token" SSE event.
func extractSSEText(t *testing.T, sse string) string {
	t.Helper()
	for _, line := range strings.Split(sse, "\n") {
		if strings.HasPrefix(line, "data: ") {
			var ev map[string]any
			if err := json.Unmarshal([]byte(line[6:]), &ev); err == nil {
				if content, ok := ev["Content"].(map[string]any); ok {
					if parts, ok := content["parts"].([]any); ok && len(parts) > 0 {
						if part, ok := parts[0].(map[string]any); ok {
							if text, ok := part["text"].(string); ok {
								return text
							}
						}
					}
				}
			}
		}
	}
	return ""
}

// findAgentIDByPrefix scans the agents list for an id starting with prefix.
func findAgentIDByPrefix(t *testing.T, ts *httptest.Server, prefix string) string {
	t.Helper()
	resp, err := http.Get(ts.URL + "/api/v1/agents")
	require.NoError(t, err)
	defer resp.Body.Close()
	var agents []map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&agents))
	for _, a := range agents {
		if id, ok := a["id"].(string); ok && strings.HasPrefix(id, prefix) {
			return id
		}
	}
	return ""
}

// waitForAgentReady polls the server until the agent's socket is available
// (the spawn handshake completes).
func waitForAgentReady(t *testing.T, srv *server.Server, agentID string) {
	t.Helper()
	require.Eventually(t, func() bool {
		return srv.AgentSocket(agentID) != ""
	}, 5*time.Second, 10*time.Millisecond)
}

// TestIntegration_ProjectCreateAndLifecycle exercises the full project
// lifecycle over the real API: create → get → list → pause → resume →
// finish. It requires the horde binary for agent subprocess spawning.
func TestIntegration_ProjectCreateAndLifecycle(t *testing.T) {
	exe := findHordeBinary(t)

	srv, err := server.New(server.Config{
		AgentCommand:       exe,
		SocketDir:          "/tmp",
		ReadyTimeout:       10 * time.Second,
		HealthPollInterval: 0,
		SpawnDefaultAgent:  false,
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
	c := newProjectAPIClient(t, ts)

	// Create a project with the greeter agent.
	code, resp := c.create("test-project", []string{"greeter"})
	require.Equal(t, http.StatusCreated, code, "create should return 201")
	projectID, _ := resp["id"].(string)
	require.NotEmpty(t, projectID)
	assert.Equal(t, "test-project", resp["name"])
	assert.Equal(t, "active", resp["state"])

	// The team should have one agent (greeter) with a spawned id.
	team, _ := resp["team"].(map[string]any)
	agentsList, _ := team["agents"].([]any)
	require.Len(t, agentsList, 1)

	// Get the project.
	code, resp = c.get(projectID)
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, projectID, resp["id"])

	// List projects.
	code, list := c.list()
	require.Equal(t, http.StatusOK, code)
	require.Len(t, list, 1)
	assert.Equal(t, projectID, list[0]["id"])

	// Pause.
	code, resp = c.postAction(projectID, "pause")
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, "paused", resp["state"])

	// Resume.
	code, resp = c.postAction(projectID, "resume")
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, "active", resp["state"])

	// Finish.
	code, resp = c.postAction(projectID, "finish")
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, "finished", resp["state"])

	// After finish, the agent's active project should be cleared.
	// Find the spawned agent and verify.
	for _, a := range srv.Agents() {
		assert.Equal(t, "", srv.AgentActiveProject(a.ID),
			"agent %s should have no active project after finish", a.ID)
	}
}

// TestIntegration_ProjectAssignAndRemove tests assigning a new agent to an
// existing project and removing it.
func TestIntegration_ProjectAssignAndRemove(t *testing.T) {
	exe := findHordeBinary(t)

	srv, err := server.New(server.Config{
		AgentCommand:       exe,
		SocketDir:          "/tmp",
		ReadyTimeout:       10 * time.Second,
		HealthPollInterval: 0,
		SpawnDefaultAgent:  false,
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
	c := newProjectAPIClient(t, ts)

	// Create with greeter.
	code, resp := c.create("assign-test", []string{"greeter"})
	require.Equal(t, http.StatusCreated, code)
	projectID := resp["id"].(string)

	// Assign a repeater agent.
	code, resp = c.assignAgent(projectID, "repeater")
	require.Equal(t, http.StatusOK, code)
	team, _ := resp["team"].(map[string]any)
	agentsList, _ := team["agents"].([]any)
	assert.Len(t, agentsList, 2, "team should have 2 agents after assignment")

	// Find the repeater agent and verify it has an active project.
	var repeaterID string
	for _, a := range srv.Agents() {
		if a.Name == "repeater" {
			repeaterID = a.ID
			break
		}
	}
	require.NotEmpty(t, repeaterID)
	assert.Equal(t, projectID, srv.AgentActiveProject(repeaterID))

	// Remove the repeater from the project.
	delResp, err := http.Post(
		ts.URL+"/api/v1/projects/"+projectID+"/agents/"+repeaterID,
		"application/json", nil,
	)
	// chi expects DELETE, not POST, for the remove endpoint. Use a direct
	// request instead.
	_ = delResp
	delReq, derr := http.NewRequest(http.MethodDelete,
		ts.URL+"/api/v1/projects/"+projectID+"/agents/"+repeaterID, nil)
	require.NoError(t, err)
	delResp2, derr := http.DefaultClient.Do(delReq)
	require.NoError(t, derr)
	defer delResp2.Body.Close()
	assert.Equal(t, http.StatusNoContent, delResp2.StatusCode)

	// The repeater's active project should be cleared.
	assert.Equal(t, "", srv.AgentActiveProject(repeaterID))
}

// TestIntegration_MultiTurnContext_WithProject verifies that invoking an
// agent assigned to a project uses a stable session key, so the repeater
// agent sees its prior turn and increments the turn count on the second
// invocation.
func TestIntegration_MultiTurnContext_WithProject(t *testing.T) {
	exe := findHordeBinary(t)

	srv, err := server.New(server.Config{
		AgentCommand:       exe,
		SocketDir:          "/tmp",
		ReadyTimeout:       10 * time.Second,
		HealthPollInterval: 0,
		SpawnDefaultAgent:  false,
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
	c := newProjectAPIClient(t, ts)

	// Create a project with the repeater agent (it counts turns via session
	// history).
	code, resp := c.create("multi-turn", []string{"repeater"})
	require.Equal(t, http.StatusCreated, code)
	projectID := resp["id"].(string)

	// Find the spawned repeater agent.
	var agentID string
	require.Eventually(t, func() bool {
		for _, a := range srv.Agents() {
			if a.Name == "repeater" {
				agentID = a.ID
				return true
			}
		}
		return false
	}, 5*time.Second, 50*time.Millisecond)
	waitForAgentReady(t, srv, agentID)

	// Verify the session key is agent_id:project_id.
	assert.Equal(t, agentID+":"+projectID, srv.SessionKey(agentID))

	// First invoke — should be turn 1.
	code, sse := c.invoke(agentID, "first message")
	require.Equal(t, http.StatusOK, code, "first invoke should succeed")
	text := extractSSEText(t, sse)
	assert.Contains(t, text, "[turn 1]", "first invoke should be turn 1")

	// Second invoke — should be turn 2 (same session key → conversation
	// history retained).
	code, sse = c.invoke(agentID, "second message")
	require.Equal(t, http.StatusOK, code, "second invoke should succeed")
	text = extractSSEText(t, sse)
	assert.Contains(t, text, "[turn 2]", "second invoke should be turn 2 (multi-turn context)")
}

// TestIntegration_MultiTurnContext_NoProject verifies that invoking an agent
// with no active project falls back to per-invocation sessions: each invoke
// is turn 1, no conversation continuity.
func TestIntegration_MultiTurnContext_NoProject(t *testing.T) {
	exe := findHordeBinary(t)

	srv, err := server.New(server.Config{
		AgentCommand:       exe,
		SocketDir:          "/tmp",
		ReadyTimeout:       10 * time.Second,
		HealthPollInterval: 0,
		SpawnDefaultAgent:  false,
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

	// Spawn a repeater agent directly (no project).
	agentID, err := srv.SpawnAgent(context.Background(), "repeater")
	require.NoError(t, err)
	waitForAgentReady(t, srv, agentID)

	// No active project → empty session key.
	assert.Equal(t, "", srv.SessionKey(agentID))

	c := newProjectAPIClient(t, ts)

	// First invoke — turn 1.
	code, sse := c.invoke(agentID, "first")
	require.Equal(t, http.StatusOK, code)
	text := extractSSEText(t, sse)
	assert.Contains(t, text, "[turn 1]")

	// Second invoke — still turn 1 (no session continuity).
	code, sse = c.invoke(agentID, "second")
	require.Equal(t, http.StatusOK, code)
	text = extractSSEText(t, sse)
	assert.Contains(t, text, "[turn 1]", "no-project invoke should not retain history")
}

// TestIntegration_ExecutionContextSeeding verifies that creating a project
// and assigning agents seeds the execution context's Project field.
func TestIntegration_ExecutionContextSeeding(t *testing.T) {
	exe := findHordeBinary(t)

	srv, err := server.New(server.Config{
		AgentCommand:       exe,
		SocketDir:          "/tmp",
		ReadyTimeout:       10 * time.Second,
		HealthPollInterval: 0,
		SpawnDefaultAgent:  false,
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
	c := newProjectAPIClient(t, ts)

	// Create a project with greeter.
	code, resp := c.create("ctx-seed-test", []string{"greeter"})
	require.Equal(t, http.StatusCreated, code)
	projectID := resp["id"].(string)

	// Find the spawned agent.
	var agentID string
	require.Eventually(t, func() bool {
		for _, a := range srv.Agents() {
			if a.Name == "greeter" {
				agentID = a.ID
				return true
			}
		}
		return false
	}, 5*time.Second, 50*time.Millisecond)

	// The execution context should have Project set to the project id.
	ctx := srv.AgentContext(agentID)
	require.NotNil(t, ctx)
	assert.Equal(t, projectID, ctx.Project, "execution context should be seeded with project id")
}

// TestIntegration_PausedProjectRejectsInvoke verifies that invoking an agent
// on a paused project returns 409 Conflict.
func TestIntegration_PausedProjectRejectsInvoke(t *testing.T) {
	exe := findHordeBinary(t)

	srv, err := server.New(server.Config{
		AgentCommand:       exe,
		SocketDir:          "/tmp",
		ReadyTimeout:       10 * time.Second,
		HealthPollInterval: 0,
		SpawnDefaultAgent:  false,
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
	c := newProjectAPIClient(t, ts)

	// Create a project with the greeter agent.
	code, resp := c.create("paused-invoke", []string{"greeter"})
	require.Equal(t, http.StatusCreated, code)
	projectID := resp["id"].(string)

	// Find the spawned agent.
	var agentID string
	require.Eventually(t, func() bool {
		for _, a := range srv.Agents() {
			if a.Name == "greeter" {
				agentID = a.ID
				return true
			}
		}
		return false
	}, 5*time.Second, 50*time.Millisecond)
	waitForAgentReady(t, srv, agentID)

	// Pause the project.
	code, _ = c.postAction(projectID, "pause")
	require.Equal(t, http.StatusOK, code)

	// Invoke should return 409.
	respBody, err := http.Post(
		ts.URL+"/api/v1/agents/"+agentID+"/invoke",
		"application/json",
		strings.NewReader(`{"message":"hello"}`),
	)
	require.NoError(t, err)
	defer respBody.Body.Close()
	assert.Equal(t, http.StatusConflict, respBody.StatusCode)

	// Resume and verify invoke works again.
	code, _ = c.postAction(projectID, "resume")
	require.Equal(t, http.StatusOK, code)

	respBody2, err := http.Post(
		ts.URL+"/api/v1/agents/"+agentID+"/invoke",
		"application/json",
		strings.NewReader(`{"message":"hello"}`),
	)
	require.NoError(t, err)
	defer respBody2.Body.Close()
	assert.Equal(t, http.StatusOK, respBody2.StatusCode)
}

// TestIntegration_ReassignmentMovesAgent verifies that when an already-
// spawned agent is assigned to a different project, it is removed from the
// old project's team. This tests the assignAgentToProject reassignment path
// directly.
func TestIntegration_ReassignmentMovesAgent(t *testing.T) {
	exe := findHordeBinary(t)

	srv, err := server.New(server.Config{
		AgentCommand:       exe,
		SocketDir:          "/tmp",
		ReadyTimeout:       10 * time.Second,
		HealthPollInterval: 0,
		SpawnDefaultAgent:  false,
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
	c := newProjectAPIClient(t, ts)

	// Create two projects.
	code, resp1 := c.create("proj-a", []string{"greeter"})
	require.Equal(t, http.StatusCreated, code)
	projA := resp1["id"].(string)

	code, resp2 := c.create("proj-b", []string{"greeter"})
	require.Equal(t, http.StatusCreated, code)
	projB := resp2["id"].(string)

	// Find proj-a's greeter agent. Both projects have a "greeter", so select
	// it from proj-a's team (the create response carries the spawned agent id)
	// rather than the first agent named "greeter" across the whole node, whose
	// order is nondeterministic.
	teamA0, _ := resp1["team"].(map[string]any)
	agentsA0, _ := teamA0["agents"].([]any)
	require.Len(t, agentsA0, 1)
	agentID, _ := agentsA0[0].(map[string]any)["agent_id"].(string)
	require.NotEmpty(t, agentID)
	waitForAgentReady(t, srv, agentID)

	// Initially the agent is active on proj-a.
	require.Equal(t, projA, srv.AgentActiveProject(agentID))

	// Verify proj-a has 1 agent in its team.
	_, projADetail := c.get(projA)
	teamA, _ := projADetail["team"].(map[string]any)
	agentsA, _ := teamA["agents"].([]any)
	require.Len(t, agentsA, 1)

	// Now rebind the agent to proj-b via the internal method. This is what
	// happens when an agent is reassigned between projects.
	srv.ReassignAgent(agentID, projB)

	// The agent's active project should now be proj-b.
	assert.Equal(t, projB, srv.AgentActiveProject(agentID))

	// proj-a's team should no longer list this agent.
	_, projADetail = c.get(projA)
	teamA, _ = projADetail["team"].(map[string]any)
	agentsA, _ = teamA["agents"].([]any)
	assert.Empty(t, agentsA, "proj-a team should be empty after reassignment")

	// proj-b's team should now list this agent.
	_, projBDetail := c.get(projB)
	teamB, _ := projBDetail["team"].(map[string]any)
	agentsB, _ := teamB["agents"].([]any)
	assert.Len(t, agentsB, 2, "proj-b team should have 2 agents (original + reassigned)")
}

// TestIntegration_FinishedProjectContextEviction verifies that finishing a
// project marks the agent's execution context as exited.
func TestIntegration_FinishedProjectContextEviction(t *testing.T) {
	exe := findHordeBinary(t)

	srv, err := server.New(server.Config{
		AgentCommand:       exe,
		SocketDir:          "/tmp",
		ReadyTimeout:       10 * time.Second,
		HealthPollInterval: 0,
		SpawnDefaultAgent:  false,
		ContextRetention:   0, // no auto-eviction; we check the lifecycle
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
	c := newProjectAPIClient(t, ts)

	// Create a project with the greeter agent.
	code, resp := c.create("finish-evict", []string{"greeter"})
	require.Equal(t, http.StatusCreated, code)
	projectID := resp["id"].(string)

	// Find the spawned agent.
	var agentID string
	require.Eventually(t, func() bool {
		for _, a := range srv.Agents() {
			if a.Name == "greeter" {
				agentID = a.ID
				return true
			}
		}
		return false
	}, 5*time.Second, 50*time.Millisecond)

	// Verify context is seeded with the project.
	ctx := srv.AgentContext(agentID)
	require.NotNil(t, ctx)
	assert.Equal(t, projectID, ctx.Project)
	assert.Equal(t, server.AgentRunning, ctx.Lifecycle)

	// Finish the project.
	code, _ = c.postAction(projectID, "finish")
	require.Equal(t, http.StatusOK, code)

	// The agent's context should be marked exited.
	ctx = srv.AgentContext(agentID)
	require.NotNil(t, ctx)
	assert.Equal(t, server.AgentExited, ctx.Lifecycle, "context should be exited after project finish")

	// The agent's active project should be cleared.
	assert.Equal(t, "", srv.AgentActiveProject(agentID))
}

// TestIntegration_DefaultWorkspace verifies that when a project is created
// without a workspace, the configured default is used and written onto the
// project record.
func TestIntegration_DefaultWorkspace(t *testing.T) {
	exe := findHordeBinary(t)
	tmp := t.TempDir()

	srv, err := server.New(server.Config{
		AgentCommand:        exe,
		SocketDir:           "/tmp",
		ReadyTimeout:        10 * time.Second,
		HealthPollInterval:  0,
		SpawnDefaultAgent:   false,
		ProjectWorkspaceDir: tmp,
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
	c := newProjectAPIClient(t, ts)

	// Create a project without specifying a workspace.
	code, body := c.do(http.MethodPost, "/api/v1/projects/", map[string]any{
		"name":   "default-ws",
		"goal":   "test goal",
		"agents": []string{"greeter"},
	})
	require.Equal(t, http.StatusCreated, code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(body, &resp))

	// The project record should have the resolved default workspace.
	workspace, _ := resp["workspace"].(string)
	assert.Equal(t, tmp, workspace, "project record should carry the resolved default workspace")

	// The KB should have been scaffolded at the default workspace.
	assert.DirExists(t, filepath.Join(tmp, ".horde", "knowledgebase"))
}
