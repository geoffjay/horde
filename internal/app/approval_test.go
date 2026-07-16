package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/geoffjay/horde/internal/client"
)

// agentViewModel builds a connected Model on the agent view with one project
// whose team contains agent "a1", carrying the given pending approvals.
func agentViewModel(addr string, approvals []client.ApprovalRef) *Model {
	m := New(context.Background(), addr)
	m.connected = true
	m.view = viewAgent
	m.selectedProjectID = "p1"
	m.cursor = 0
	m.projects = []client.Project{{ID: "p1", Name: "auth", State: "active", Team: client.ProjectTeam{
		Agents: []client.TeamAgent{{AgentID: "a1", Name: "coder"}},
	}}}
	m.contexts = map[string]client.ExecutionContext{
		"a1": {AgentID: "a1", PendingApprovals: approvals},
	}
	return m
}

func TestApprovalDecisionCmd_SendsDecision(t *testing.T) {
	var gotPath, gotDecision string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		gotDecision = body["decision"]
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	m := agentViewModel(srv.Listener.Addr().String(), []client.ApprovalRef{
		{RequestID: "req-1", ToolName: "write_file"},
	})

	cmd := m.approvalDecisionCmd(approvalAllow)
	require.NotNil(t, cmd, "a pending approval should produce a decision command")
	msg := cmd()
	res, ok := msg.(approvalActionMsg)
	require.True(t, ok)
	require.NoError(t, res.err)
	assert.Equal(t, "/api/v1/agents/a1/approvals/req-1", gotPath)
	assert.Equal(t, "allow", gotDecision)
}

func TestApprovalDecisionCmd_NilWhenNoApproval(t *testing.T) {
	m := agentViewModel("127.0.0.1:1", nil)
	assert.Nil(t, m.approvalDecisionCmd(approvalDeny), "no pending approval → no command")
}

func TestMoveSelection_MovesApprovalCursorOnAgentView(t *testing.T) {
	m := agentViewModel("127.0.0.1:1", []client.ApprovalRef{
		{RequestID: "r1", ToolName: "write_file"},
		{RequestID: "r2", ToolName: "bash"},
	})

	m.moveSelection(1)
	assert.Equal(t, 1, m.approvalCursor, "down should advance the approval cursor")
	assert.Equal(t, 0, m.cursor, "the agent cursor should be untouched")

	sel, ok := m.selectedApproval()
	require.True(t, ok)
	assert.Equal(t, "r2", sel.RequestID)

	// Clamped at the end.
	m.moveSelection(1)
	assert.Equal(t, 1, m.approvalCursor)
}

func TestMoveSelection_MovesListCursorWhenNoApprovals(t *testing.T) {
	m := agentViewModel("127.0.0.1:1", nil)
	// Two agents so the list cursor has room to move.
	m.projects[0].Team.Agents = append(m.projects[0].Team.Agents, client.TeamAgent{AgentID: "a2", Name: "tester"})

	m.moveSelection(1)
	assert.Equal(t, 1, m.cursor, "with no approvals, down moves the agent cursor")
	assert.Equal(t, 0, m.approvalCursor)
}

func TestAgentViewKey_AllowFiresDecision(t *testing.T) {
	var hit bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hit = true
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	m := agentViewModel(srv.Listener.Addr().String(), []client.ApprovalRef{
		{RequestID: "req-1", ToolName: "write_file"},
	})

	_, cmd := m.Update(keyPress("a"))
	require.NotNil(t, cmd, "'a' on the agent view with a pending approval should fire a command")
	cmd()
	assert.True(t, hit, "the decision command should call the approvals endpoint")
}

func TestAgentViewKey_AllowNoopWithoutApproval(t *testing.T) {
	m := agentViewModel("127.0.0.1:1", nil)
	_, cmd := m.Update(keyPress("a"))
	assert.Nil(t, cmd, "'a' with no pending approval should be a no-op")
}
