package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/geoffjay/horde/internal/server"
)

func TestGetAgentContext_NotFound(t *testing.T) {
	srv := newTestServer(t)
	h := Router(srv)

	w := do(t, h, http.MethodGet, "/api/v1/agents/nonexistent/context", nil)
	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestGetAgentContext_AfterSpawn(t *testing.T) {
	// SpawnAgent seeds the context; but SpawnDefaultAgent is false in
	// newTestServer and a real subprocess is needed. The context store
	// unit tests in internal/server cover materialization; the integration
	// test (phase3_test.go) covers the spawn → context path end-to-end.
	t.Skip("context store is seeded by SpawnAgent which needs a real subprocess; see integration tests")
}

func TestListAgentContexts_Empty(t *testing.T) {
	srv := newTestServer(t)
	h := Router(srv)

	w := do(t, h, http.MethodGet, "/api/v1/agents/context", nil)
	require.Equal(t, http.StatusOK, w.Code)

	var ctxs []server.ExecutionContext
	require.NoError(t, json.NewDecoder(w.Body).Decode(&ctxs))
	assert.Empty(t, ctxs)
}

func TestStreamAgentContext_NotFound(t *testing.T) {
	srv := newTestServer(t)
	h := Router(srv)

	w := do(t, h, http.MethodGet, "/api/v1/agents/nonexistent/context/stream", nil)
	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestListRemoteAgentContexts_Empty(t *testing.T) {
	srv := newTestServer(t)
	h := Router(srv)

	w := do(t, h, http.MethodGet, "/api/v1/cluster/agents/context", nil)
	require.Equal(t, http.StatusOK, w.Code)

	var ctxs []server.ExecutionContext
	require.NoError(t, json.NewDecoder(w.Body).Decode(&ctxs))
	assert.Empty(t, ctxs)
}

func TestListRemoteAgentContexts_WithFilter(t *testing.T) {
	srv := newTestServer(t)
	h := Router(srv)

	// Report some remote contexts via the master.
	srv.ReportContexts("slave-1", []server.ExecutionContext{
		{AgentID: "a-1", NodeID: "slave-1", Issue: "bug-42", Project: "p1"},
		{AgentID: "a-2", NodeID: "slave-1", Issue: "bug-99", Project: "p1"},
	})

	w := do(t, h, http.MethodGet, "/api/v1/cluster/agents/context?issue=bug-42", nil)
	require.Equal(t, http.StatusOK, w.Code)

	var ctxs []server.ExecutionContext
	require.NoError(t, json.NewDecoder(w.Body).Decode(&ctxs))
	require.Len(t, ctxs, 1)
	assert.Equal(t, "bug-42", ctxs[0].Issue)
}

func TestListRemoteAgentContexts_Redacted(t *testing.T) {
	srv := newTestServer(t)
	h := Router(srv)

	// Report a context with sensitive fields.
	srv.ReportContexts("slave-1", []server.ExecutionContext{
		{
			AgentID:       "a-1",
			NodeID:        "slave-1",
			Blocked:       true,
			BlockedReason: "sensitive",
			Note:          "secret note",
			TurnID:        "turn-1",
		},
	})

	w := do(t, h, http.MethodGet, "/api/v1/cluster/agents/context", nil)
	require.Equal(t, http.StatusOK, w.Code)

	var ctxs []server.ExecutionContext
	require.NoError(t, json.NewDecoder(w.Body).Decode(&ctxs))
	require.Len(t, ctxs, 1)
	assert.True(t, ctxs[0].Blocked)
	assert.Empty(t, ctxs[0].BlockedReason, "blocked_reason should be redacted")
	assert.Empty(t, ctxs[0].Note, "note should be redacted")
	assert.Empty(t, ctxs[0].TurnID, "turn_id should be redacted")
}

func TestListRemoteAgentContexts_Counts(t *testing.T) {
	srv := newTestServer(t)
	h := Router(srv)

	srv.ReportContexts("slave-1", []server.ExecutionContext{
		{AgentID: "a-1", NodeID: "slave-1", ErrorCount: 2, PendingApprovalCount: 1},
	})

	w := do(t, h, http.MethodGet, "/api/v1/cluster/agents/context", nil)
	require.Equal(t, http.StatusOK, w.Code)

	var ctxs []server.ExecutionContext
	require.NoError(t, json.NewDecoder(w.Body).Decode(&ctxs))
	require.Len(t, ctxs, 1)
	assert.Equal(t, 2, ctxs[0].ErrorCount, "error count survives redaction")
	assert.Equal(t, 1, ctxs[0].PendingApprovalCount, "approval count survives redaction")
}

func TestIsLoopbackRequest(t *testing.T) {
	cases := map[string]bool{
		"127.0.0.1:5000": true,
		"[::1]:5000":     true,
		"10.0.0.5:5000":  false,
		"":               false,
	}
	for addr, want := range cases {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.RemoteAddr = addr
		assert.Equal(t, want, isLoopbackRequest(r), "addr %q", addr)
	}
}

func TestProjectContext_Redacts(t *testing.T) {
	ctx := &server.ExecutionContext{
		AgentID:          "a-1",
		Blocked:          true,
		BlockedReason:    "secret",
		Note:             "note",
		TurnID:           "t1",
		Errors:           []server.ErrorSummary{{Code: "E001"}},
		PendingApprovals: []server.ApprovalRef{{RequestID: "r1"}},
	}

	// Full view returns the context unchanged.
	assert.Same(t, ctx, projectContext(ctx, true))

	// Redacted view drops sensitive fields but keeps the counts.
	red := projectContext(ctx, false)
	assert.NotSame(t, ctx, red)
	assert.True(t, red.Blocked)
	assert.Empty(t, red.BlockedReason)
	assert.Empty(t, red.Note)
	assert.Empty(t, red.TurnID)
	assert.Empty(t, red.Errors)
	assert.Empty(t, red.PendingApprovals)
	assert.Equal(t, 1, red.ErrorCount)
	assert.Equal(t, 1, red.PendingApprovalCount)
}

func TestHeartbeat_WithContexts(t *testing.T) {
	srv := newTestServer(t)
	h := Router(srv)

	// Register first.
	w := do(t, h, http.MethodPost, "/api/v1/cluster/register", registerRequest{
		NodeID: "slave-1", Mode: "slave", Addr: "slave1:13420",
	})
	require.Equal(t, http.StatusOK, w.Code)

	now := time.Now().UTC()
	w = do(t, h, http.MethodPost, "/api/v1/cluster/heartbeat", heartbeatRequest{
		NodeID: "slave-1",
		Agents: []string{"greeter"},
		Contexts: []server.ExecutionContextDigest{
			{
				AgentID:   "a-1",
				Project:   "p1",
				Issue:     "bug-1",
				Activity:  server.StateBusy,
				Lifecycle: server.AgentRunning,
				UpdatedAt: now,
			},
		},
	})
	require.Equal(t, http.StatusOK, w.Code)

	// The remote context should be visible via the cluster context API.
	w = do(t, h, http.MethodGet, "/api/v1/cluster/agents/context", nil)
	require.Equal(t, http.StatusOK, w.Code)

	var ctxs []server.ExecutionContext
	require.NoError(t, json.NewDecoder(w.Body).Decode(&ctxs))
	require.Len(t, ctxs, 1)
	assert.Equal(t, "a-1", ctxs[0].AgentID)
	assert.Equal(t, "bug-1", ctxs[0].Issue)
	assert.Equal(t, server.StateBusy, ctxs[0].Activity)
}

func TestStreamAgentContext_Snapshot(t *testing.T) {
	srv := newTestServer(t)
	h := Router(srv)

	// Report a context so the endpoint has something to stream.
	srv.ReportContexts("slave-1", []server.ExecutionContext{
		{AgentID: "a-1", NodeID: "slave-1"},
	})

	// Verify the stream endpoint returns 404 for unknown local agents
	// (we can't easily test the SSE stream in httptest without a real
	// subscriber, but the 404 path confirms the route is wired).
	w := do(t, h, http.MethodGet, "/api/v1/agents/nonexistent/context/stream", nil)
	assert.Equal(t, http.StatusNotFound, w.Code)

	_ = httptest.NewRecorder // keep import used
}
