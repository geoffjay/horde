package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/geoffjay/horde/internal/client"
)

// sseTestHandler returns an http.Handler that serves a context SSE stream
// with a configurable sequence of execution context snapshots.
func sseTestHandler(snapshots []client.ExecutionContext) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/health", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})
	mux.HandleFunc("/api/v1/node", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"mode": "master", "leader_connected": true, "node_id": "n1", "version": "test",
		})
	})
	mux.HandleFunc("/api/v1/agents", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode([]client.Agent{{ID: "a1", Name: "greeter", Status: "running"}})
	})
	mux.HandleFunc("/api/v1/agents/context", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode([]client.ExecutionContext{{AgentID: "a1", Activity: client.StateIdle}})
	})
	mux.HandleFunc("/api/v1/projects/", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode([]client.Project{
			{ID: "p1", Name: "auth", State: "active", Team: client.ProjectTeam{Agents: []client.TeamAgent{
				{AgentID: "a1", Name: "greeter"},
			}}},
		})
	})
	mux.HandleFunc("/api/v1/agents/a1/context/stream", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		for _, ec := range snapshots {
			data, _ := json.Marshal(ec)
			_, _ = w.Write([]byte("event: context\n"))
			_, _ = w.Write([]byte("data: "))
			_, _ = w.Write(data)
			_, _ = w.Write([]byte("\n\n"))
			if flusher != nil {
				flusher.Flush()
			}
		}
	})
	return mux
}

func TestSubscribeAgentContext_OpensStream(t *testing.T) {
	snapshots := []client.ExecutionContext{
		{AgentID: "a1", Activity: client.StateIdle, Lifecycle: client.AgentRunning},
		{AgentID: "a1", Activity: client.StateBusy, Lifecycle: client.AgentRunning},
	}
	srv := httptest.NewServer(sseTestHandler(snapshots))
	defer srv.Close()

	m := New(context.Background(), srv.Listener.Addr().String())
	m.Update(m.connect())
	m.Update(m.loadNode())
	m.connected = true

	// Drill into the project, then into the agent — this triggers the SSE
	// subscription via drillIn.
	m.Update(namedKey(tea.KeyEnter)) // projects → projectDetail
	require.Equal(t, viewProjectDetail, m.view)

	_, cmd := m.Update(namedKey(tea.KeyEnter)) // projectDetail → agent
	require.Equal(t, viewAgent, m.view)
	require.NotNil(t, m.streamCancel, "streamCancel should be set after subscribing")
	require.NotNil(t, m.streamCh, "streamCh should be set after subscribing")

	// Pump the first context delta from the stream.
	if cmd != nil {
		msg := cmd()
		delta, ok := msg.(contextDeltaMsg)
		require.True(t, ok, "first pump should return a contextDeltaMsg")
		assert.Equal(t, "a1", delta.ctx.AgentID)
		assert.Equal(t, client.StateIdle, delta.ctx.Activity)

		// Update stores the delta and sets streamConnected.
		m.Update(delta)
		assert.True(t, m.streamConnected, "streamConnected should be true after first delta")
		assert.Equal(t, client.StateIdle, m.contexts["a1"].Activity)
	}
}

func TestUnsubscribeOnPopView(t *testing.T) {
	srv := httptest.NewServer(sseTestHandler([]client.ExecutionContext{
		{AgentID: "a1", Activity: client.StateIdle},
	}))
	defer srv.Close()

	m := New(context.Background(), srv.Listener.Addr().String())
	m.Update(m.connect())
	m.Update(m.loadNode())
	m.connected = true

	// Drill into agent view.
	m.Update(namedKey(tea.KeyEnter))
	m.Update(namedKey(tea.KeyEnter))
	require.Equal(t, viewAgent, m.view)
	require.NotNil(t, m.streamCancel)

	// Esc pops back — should unsubscribe.
	m.Update(escKey())
	assert.Equal(t, viewProjectDetail, m.view)
	assert.Nil(t, m.streamCancel, "streamCancel should be nil after popView")
	assert.Nil(t, m.streamCh, "streamCh should be nil after popView")
	assert.False(t, m.streamConnected, "streamConnected should be false after popView")
}

func TestUnsubscribeOnGoHome(t *testing.T) {
	srv := httptest.NewServer(sseTestHandler([]client.ExecutionContext{
		{AgentID: "a1", Activity: client.StateIdle},
	}))
	defer srv.Close()

	m := New(context.Background(), srv.Listener.Addr().String())
	m.Update(m.connect())
	m.Update(m.loadNode())
	m.connected = true

	m.Update(namedKey(tea.KeyEnter))
	m.Update(namedKey(tea.KeyEnter))
	require.Equal(t, viewAgent, m.view)
	require.NotNil(t, m.streamCancel)

	m.goHome()
	assert.Nil(t, m.streamCancel, "streamCancel should be nil after goHome")
	assert.False(t, m.streamConnected)
}

func TestContextDeltaMsg_UpdatesContextsMap(t *testing.T) {
	m := New(context.Background(), "127.0.0.1:1")
	m.contexts = map[string]client.ExecutionContext{
		"a1": {AgentID: "a1", Activity: client.StateIdle},
	}

	delta := contextDeltaMsg{ctx: client.ExecutionContext{
		AgentID: "a1", Activity: client.StateBusy, Issue: "#142",
	}}
	m.Update(delta)

	assert.Equal(t, client.StateBusy, m.contexts["a1"].Activity)
	assert.Equal(t, "#142", m.contexts["a1"].Issue)
	assert.True(t, m.streamConnected)
}

func TestStreamErrMsg_ClearsStreamConnected(t *testing.T) {
	m := New(context.Background(), "127.0.0.1:1")
	m.streamConnected = true
	m.streamCh = make(<-chan client.ExecutionContext)

	m.Update(streamErrMsg{})

	assert.False(t, m.streamConnected)
	assert.Nil(t, m.streamCh)
}

func TestLiveStatusBlock_WhenConnected(t *testing.T) {
	m := New(context.Background(), "127.0.0.1:1")
	m.streamConnected = true

	block := liveStatusBlock()
	out := block.Render(m)
	assert.Contains(t, out, "live")
}

func TestLiveStatusBlock_WhenDisconnected(t *testing.T) {
	m := New(context.Background(), "127.0.0.1:1")
	m.streamConnected = false

	block := liveStatusBlock()
	assert.Empty(t, block.Render(m))
}

func TestRenderAgentView_HeaderAndFields(t *testing.T) {
	m := New(context.Background(), "127.0.0.1:1")
	m.connected = true
	m.view = viewAgent
	m.selectedProjectID = "p1"
	m.cursor = 0
	m.projects = []client.Project{{ID: "p1", Name: "auth", State: "active", Team: client.ProjectTeam{Agents: []client.TeamAgent{
		{AgentID: "a1", Name: "reviewer"},
	}}}}
	m.contexts = map[string]client.ExecutionContext{
		"a1": {
			AgentID:       "a1",
			Activity:      client.StateIdle,
			Project:       "auth",
			Issue:         "#150",
			TurnID:        "t-88",
			Lifecycle:     client.AgentRunning,
			Blocked:       true,
			BlockedReason: "awaiting tool approval",
			Errors: []client.ErrorSummary{
				{Code: "E_TOOL", Message: "permission denied", Fatal: true},
				{Code: "E_MODEL", Message: "rate limited"},
			},
			PendingApprovals: []client.ApprovalRef{
				{RequestID: "7c2e1234abcd", ToolName: "write_file"},
			},
			Note: "paused pending human review",
		},
	}

	out := m.renderAgentView()
	assert.Contains(t, out, "reviewer")
	assert.Contains(t, out, "blocked")
	assert.Contains(t, out, "auth")
	assert.Contains(t, out, "#150")
	assert.Contains(t, out, "turn t-88")
	assert.Contains(t, out, "lifecycle running")
	assert.Contains(t, out, "awaiting tool approval")
	assert.Contains(t, out, "Pending approvals (1)")
	assert.Contains(t, out, "write_file")
	assert.Contains(t, out, "7c2e123…", "request id should be truncated")
	assert.Contains(t, out, "Errors (2)")
	assert.Contains(t, out, "E_TOOL")
	assert.Contains(t, out, "permission denied")
	assert.Contains(t, out, "fatal")
	assert.Contains(t, out, "paused pending human review")
}

func TestRenderAgentView_NoAgentSelected(t *testing.T) {
	m := New(context.Background(), "127.0.0.1:1")
	m.connected = true
	m.view = viewAgent
	m.projects = nil

	out := m.renderAgentView()
	assert.Contains(t, out, "no agent selected")
}

func TestRenderAgentView_WaitingOnModel(t *testing.T) {
	m := New(context.Background(), "127.0.0.1:1")
	m.connected = true
	m.view = viewAgent
	m.selectedProjectID = "p1"
	m.cursor = 0
	m.projects = []client.Project{{ID: "p1", Name: "auth", State: "active", Team: client.ProjectTeam{Agents: []client.TeamAgent{
		{AgentID: "a1", Name: "coder"},
	}}}}
	m.contexts = map[string]client.ExecutionContext{
		"a1": {AgentID: "a1", Activity: client.StateIdle, WaitingModel: true},
	}

	out := m.renderAgentView()
	assert.Contains(t, out, "waiting on model: yes")
}

func TestRenderAgentView_RedactedContext(t *testing.T) {
	m := New(context.Background(), "127.0.0.1:1")
	m.connected = true
	m.view = viewAgent
	m.selectedProjectID = "p1"
	m.cursor = 0
	m.projects = []client.Project{{ID: "p1", Name: "auth", State: "active", Team: client.ProjectTeam{Agents: []client.TeamAgent{
		{AgentID: "a1", Name: "deployer"},
	}}}}
	m.contexts = map[string]client.ExecutionContext{
		"a1": {AgentID: "a1", Activity: client.StateIdle, ErrorCount: 2, PendingApprovalCount: 1},
	}

	out := m.renderAgentView()
	assert.Contains(t, out, "Errors (2)")
	assert.Contains(t, out, "Pending approvals (1)")
}

func TestTruncateID(t *testing.T) {
	assert.Equal(t, "7c2e123…", truncateID("7c2e1234abcd"))
	assert.Equal(t, "short", truncateID("short"))
	assert.Equal(t, "", truncateID(""))
}
