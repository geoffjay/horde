package app

import (
	"context"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/geoffjay/horde/internal/client"
)

func TestProjectsView_RendersProjectRows(t *testing.T) {
	m := New(context.Background(), "127.0.0.1:1")
	m.connected = true
	m.projects = []client.Project{
		{ID: "p1", Name: "auth-service", State: "active", Goal: "Fix login", Team: client.ProjectTeam{Agents: []client.TeamAgent{{AgentID: "a1", Name: "greeter"}}}},
		{ID: "p2", Name: "billing", State: "paused", Goal: "Migrate to Stripe", Team: client.ProjectTeam{Agents: []client.TeamAgent{{AgentID: "a2", Name: "coder"}}}},
	}
	m.width, m.height = 100, 24

	out := m.renderProjectsView()
	assert.Contains(t, out, "auth-service")
	assert.Contains(t, out, "billing")
	assert.Contains(t, out, "active")
	assert.Contains(t, out, "paused")
	assert.Contains(t, out, "Fix login")
}

func TestProjectsView_RollupFromContexts(t *testing.T) {
	m := New(context.Background(), "127.0.0.1:1")
	m.connected = true
	m.projects = []client.Project{
		{ID: "p1", Name: "auth", State: "active", Team: client.ProjectTeam{Agents: []client.TeamAgent{
			{AgentID: "a1"}, {AgentID: "a2"}, {AgentID: "a3"}, {AgentID: "a4"},
		}}},
	}
	m.contexts = map[string]client.ExecutionContext{
		"a1": {AgentID: "a1", Activity: client.StateIdle},
		"a2": {AgentID: "a2", Activity: client.StateBusy},
		"a3": {AgentID: "a3", Activity: client.StateBusy},
		"a4": {AgentID: "a4", Activity: client.StateIdle, Blocked: true, BlockedReason: "awaiting review"},
	}

	out := m.renderProjectsView()
	// Rollup: "4 agents · 1 idle · 2 busy · 1 blocked"
	assert.Contains(t, out, "4 agents")
	assert.Contains(t, out, "1 idle")
	assert.Contains(t, out, "2 busy")
	assert.Contains(t, out, "1 blocked")
}

func TestProjectsView_RollupZeroAgents(t *testing.T) {
	m := New(context.Background(), "127.0.0.1:1")
	m.connected = true
	m.projects = []client.Project{
		{ID: "p1", Name: "empty", State: "active"},
	}

	out := m.renderProjectsView()
	assert.Contains(t, out, "0 agents")
}

func TestProjectsView_EmptyProjects(t *testing.T) {
	m := New(context.Background(), "127.0.0.1:1")
	m.connected = true
	m.projects = nil

	out := m.renderProjectsView()
	assert.Contains(t, out, "no projects")
}

func TestProjectsView_CursorHighlight(t *testing.T) {
	m := New(context.Background(), "127.0.0.1:1")
	m.connected = true
	m.projects = []client.Project{
		{ID: "p1", Name: "first", State: "active"},
		{ID: "p2", Name: "second", State: "active"},
	}
	m.cursor = 1

	out := m.renderProjectsView()
	// Both names appear; cursor is on the second row.
	assert.Contains(t, out, "first")
	assert.Contains(t, out, "second")
}

func TestProjectDetailView_RendersStateWorkspaceGoal(t *testing.T) {
	m := New(context.Background(), "127.0.0.1:1")
	m.connected = true
	m.projects = []client.Project{
		{ID: "p1", Name: "auth", State: "active", Workspace: "~/work/auth", Goal: "Fix login"},
	}
	m.cursor = 0
	m.selectedProjectID = "p1"
	m.view = viewProjectDetail

	out := m.renderProjectDetailView()
	assert.Contains(t, out, "state")
	assert.Contains(t, out, "active")
	assert.Contains(t, out, "workspace")
	assert.Contains(t, out, "~/work/auth")
	assert.Contains(t, out, "goal")
	assert.Contains(t, out, "Fix login")
	assert.Contains(t, out, "Team")
}

func TestProjectDetailView_RendersTeamAgents(t *testing.T) {
	m := New(context.Background(), "127.0.0.1:1")
	m.connected = true
	m.projects = []client.Project{
		{ID: "p1", Name: "auth", State: "active", Team: client.ProjectTeam{Agents: []client.TeamAgent{
			{AgentID: "a1", Name: "greeter"},
			{AgentID: "a2", Name: "coder"},
		}}},
	}
	m.cursor = 0
	m.selectedProjectID = "p1"
	m.view = viewProjectDetail
	m.contexts = map[string]client.ExecutionContext{
		"a1": {AgentID: "a1", Activity: client.StateIdle, Issue: "#142", TurnID: "3"},
		"a2": {AgentID: "a2", Activity: client.StateBusy, Issue: "#142"},
	}

	out := m.renderProjectDetailView()
	assert.Contains(t, out, "greeter")
	assert.Contains(t, out, "coder")
	assert.Contains(t, out, "idle")
	assert.Contains(t, out, "busy")
	assert.Contains(t, out, "#142")
	assert.Contains(t, out, "turn 3")
}

func TestProjectDetailView_AgentErrorsAndApprovals(t *testing.T) {
	m := New(context.Background(), "127.0.0.1:1")
	m.connected = true
	m.projects = []client.Project{
		{ID: "p1", Name: "auth", State: "active", Team: client.ProjectTeam{Agents: []client.TeamAgent{
			{AgentID: "a1", Name: "reviewer"},
		}}},
	}
	m.cursor = 0
	m.selectedProjectID = "p1"
	m.view = viewProjectDetail
	m.contexts = map[string]client.ExecutionContext{
		"a1": {
			AgentID: "a1", Activity: client.StateIdle, Blocked: true, BlockedReason: "awaiting approval",
			Errors:           []client.ErrorSummary{{Code: "E_TOOL", Message: "denied"}},
			PendingApprovals: []client.ApprovalRef{{RequestID: "r1", ToolName: "write_file"}},
		},
	}

	out := m.renderProjectDetailView()
	assert.Contains(t, out, "reviewer")
	assert.Contains(t, out, "1 error")
	assert.Contains(t, out, "1 approval")
	assert.Contains(t, out, "awaiting approval")
}

func TestProjectDetailView_RemoteRedactedContext(t *testing.T) {
	m := New(context.Background(), "127.0.0.1:1")
	m.connected = true
	m.projects = []client.Project{
		{ID: "p1", Name: "auth", State: "active", Team: client.ProjectTeam{Agents: []client.TeamAgent{
			{AgentID: "a1", Name: "deployer"},
		}}},
	}
	m.cursor = 0
	m.selectedProjectID = "p1"
	m.view = viewProjectDetail
	// Remote/redacted context: slices empty, only counts present.
	m.contexts = map[string]client.ExecutionContext{
		"a1": {AgentID: "a1", Activity: client.StateIdle, ErrorCount: 2, PendingApprovalCount: 1},
	}

	out := m.renderProjectDetailView()
	assert.Contains(t, out, "2 errors")
	assert.Contains(t, out, "1 approval")
}

func TestProjectDetailView_NoAgentsAssigned(t *testing.T) {
	m := New(context.Background(), "127.0.0.1:1")
	m.connected = true
	m.projects = []client.Project{
		{ID: "p1", Name: "empty", State: "active"},
	}
	m.cursor = 0
	m.selectedProjectID = "p1"
	m.view = viewProjectDetail

	out := m.renderProjectDetailView()
	assert.Contains(t, out, "no agents assigned")
}

func TestProjectDetailView_ProjectNotFound(t *testing.T) {
	m := New(context.Background(), "127.0.0.1:1")
	m.connected = true
	m.projects = nil
	m.cursor = 0
	m.selectedProjectID = "p1"
	m.view = viewProjectDetail

	out := m.renderProjectDetailView()
	assert.Contains(t, out, "not found")
}

func TestProjectDetailView_CursorOnTeamAgents(t *testing.T) {
	m := New(context.Background(), "127.0.0.1:1")
	m.connected = true
	m.projects = []client.Project{
		{ID: "p1", Name: "auth", State: "active", Team: client.ProjectTeam{Agents: []client.TeamAgent{
			{AgentID: "a1", Name: "greeter"},
			{AgentID: "a2", Name: "coder"},
		}}},
	}
	m.selectedProjectID = "p1"
	m.cursor = 1
	m.view = viewProjectDetail

	// visibleAgents returns team agents in project detail
	agents := m.visibleAgents()
	require.Len(t, agents, 2)
	assert.Equal(t, "coder", agents[1].Name)

	// selectedAgent returns the cursor-indexed team agent
	a, ok := m.selectedAgent()
	require.True(t, ok)
	assert.Equal(t, "a2", a.ID)
}

func TestProjectDetailView_DrillInUsesTeamAgent(t *testing.T) {
	stub := setupNavTestModel(t)

	// Drill into first project
	stub.model.Update(namedKey(tea.KeyEnter))
	require.Equal(t, viewProjectDetail, stub.model.view)

	// Cursor on first team agent; drill into agent view
	stub.model.Update(namedKey(tea.KeyEnter))
	assert.Equal(t, viewAgent, stub.model.view)

	// The selected agent should be the team agent, not from m.agents
	a, ok := stub.model.selectedAgent()
	require.True(t, ok)
	assert.Equal(t, "a1", a.ID)
	assert.Equal(t, "greeter", a.Name)
}

// navTestModel is a helper for view tests that need a fully loaded model.
type navTestModel struct {
	model *Model
}

func setupNavTestModel(t *testing.T) *navTestModel {
	t.Helper()
	m := New(context.Background(), "127.0.0.1:1")
	m.connected = true
	m.projects = []client.Project{
		{ID: "p1", Name: "auth", State: "active", Team: client.ProjectTeam{Agents: []client.TeamAgent{
			{AgentID: "a1", Name: "greeter"},
			{AgentID: "a2", Name: "coder"},
		}}},
	}
	m.contexts = map[string]client.ExecutionContext{
		"a1": {AgentID: "a1", Activity: client.StateIdle, Issue: "#142", TurnID: "3"},
		"a2": {AgentID: "a2", Activity: client.StateBusy, Issue: "#142"},
	}
	m.agents = []client.Agent{
		{ID: "a1", Name: "greeter", Status: "running"},
		{ID: "a2", Name: "coder", Status: "running"},
	}
	return &navTestModel{model: m}
}
