package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/geoffjay/horde/internal/client"
)

func TestOpenForm_ClearsFields(t *testing.T) {
	m := New(context.Background(), "127.0.0.1:1")

	m.openForm()
	assert.True(t, m.form.open)
	assert.Equal(t, 0, m.form.cursor)
	for _, f := range m.form.fields {
		assert.Empty(t, f)
	}
}

func TestCloseForm_ResetsState(t *testing.T) {
	m := New(context.Background(), "127.0.0.1:1")
	m.form = projectForm{open: true, cursor: 2, fields: [formFieldCount]string{"x", "y", "z", "w"}}

	m.closeForm()
	assert.False(t, m.form.open)
	assert.Equal(t, 0, m.form.cursor)
	for _, f := range m.form.fields {
		assert.Empty(t, f)
	}
}

func TestFormKey_TypesIntoActiveField(t *testing.T) {
	m := New(context.Background(), "127.0.0.1:1")
	m.openForm()

	m.handleFormKey(keyPress("a"))
	m.handleFormKey(keyPress("b"))
	assert.Equal(t, "ab", m.form.fields[formFieldName])
}

func TestFormKey_BackspaceDeletes(t *testing.T) {
	m := New(context.Background(), "127.0.0.1:1")
	m.openForm()
	m.form.fields[formFieldName] = "hello"

	m.handleFormKey(namedKey(tea.KeyBackspace))
	assert.Equal(t, "hell", m.form.fields[formFieldName])
}

func TestFormKey_TabMovesToNextField(t *testing.T) {
	m := New(context.Background(), "127.0.0.1:1")
	m.openForm()
	require.Equal(t, 0, m.form.cursor)

	m.handleFormKey(keyPress("tab"))
	assert.Equal(t, 1, m.form.cursor)

	m.handleFormKey(keyPress("tab"))
	assert.Equal(t, 2, m.form.cursor)

	m.handleFormKey(keyPress("tab"))
	assert.Equal(t, 3, m.form.cursor)

	// Tab wraps around to the first field.
	m.handleFormKey(keyPress("tab"))
	assert.Equal(t, 0, m.form.cursor)
}

func TestFormKey_ShiftTabMovesToPreviousField(t *testing.T) {
	m := New(context.Background(), "127.0.0.1:1")
	m.openForm()
	m.form.cursor = 0

	// shift+tab wraps to the last field.
	m.handleFormKey(keyPress("shift+tab"))
	assert.Equal(t, formFieldCount-1, m.form.cursor)

	// shift+tab from field 3 → field 2.
	m.handleFormKey(keyPress("shift+tab"))
	assert.Equal(t, formFieldCount-2, m.form.cursor)
}

func TestFormKey_UpDownMoveBetweenFields(t *testing.T) {
	m := New(context.Background(), "127.0.0.1:1")
	m.openForm()

	m.handleFormKey(namedKey(tea.KeyDown))
	assert.Equal(t, 1, m.form.cursor)

	m.handleFormKey(namedKey(tea.KeyDown))
	assert.Equal(t, 2, m.form.cursor)

	m.handleFormKey(namedKey(tea.KeyUp))
	assert.Equal(t, 1, m.form.cursor)
}

func TestFormKey_EscClosesForm(t *testing.T) {
	m := New(context.Background(), "127.0.0.1:1")
	m.openForm()
	require.True(t, m.form.open)

	m.handleFormKey(escKey())
	assert.False(t, m.form.open)
}

func TestFormKey_CtrlCQuits(t *testing.T) {
	m := New(context.Background(), "127.0.0.1:1")
	m.openForm()

	m.handleFormKey(ctrlKey('c'))
	assert.True(t, m.quitting)
	assert.False(t, m.form.open)
}

func TestFormKey_EnterSubmitsWhenNameFilled(t *testing.T) {
	stub := setupProjectActionServer(t, http.StatusOK, client.Project{ID: "p9", Name: "test", State: "active"})
	defer stub.Close()

	m := New(context.Background(), stub.Listener.Addr().String())
	m.openForm()
	m.form.fields[formFieldName] = "test"
	m.form.fields[formFieldWorkspace] = "~/work/test"
	m.form.fields[formFieldGoal] = "do stuff"
	m.form.fields[formFieldAgents] = "coder, reviewer"

	_, cmd := m.handleFormKey(namedKey(tea.KeyEnter))
	require.NotNil(t, cmd)
	assert.False(t, m.form.open, "form should close on submit")

	msg := cmd()
	pa, ok := msg.(projectActionMsg)
	require.True(t, ok)
	require.NoError(t, pa.err)
	assert.Equal(t, "p9", pa.project.ID)
	assert.Equal(t, "test", pa.project.Name)
}

func TestFormKey_EnterDoesNothingWhenNameEmpty(t *testing.T) {
	m := New(context.Background(), "127.0.0.1:1")
	m.openForm()
	m.form.fields[formFieldName] = "   " // whitespace only

	_, cmd := m.handleFormKey(namedKey(tea.KeyEnter))
	assert.Nil(t, cmd, "submit with empty name should not send")
	assert.True(t, m.form.open, "form should stay open on empty submit")
}

func TestParseAgentNames(t *testing.T) {
	assert.Empty(t, parseAgentNames(""))
	assert.Equal(t, []string{"coder"}, parseAgentNames("coder"))
	assert.Equal(t, []string{"coder", "reviewer"}, parseAgentNames("coder, reviewer"))
	assert.Equal(t, []string{"coder", "reviewer"}, parseAgentNames(" coder , reviewer "))
	assert.Equal(t, []string{"a", "b", "c"}, parseAgentNames("a,,b,c,"))
}

func TestSubmitForm_WhitespaceTrimmedFromName(t *testing.T) {
	stub := setupProjectActionServer(t, http.StatusOK, client.Project{ID: "p1", Name: "auth", State: "active"})
	defer stub.Close()

	m := New(context.Background(), stub.Listener.Addr().String())
	m.openForm()
	m.form.fields[formFieldName] = "  auth  "
	m.form.fields[formFieldAgents] = " coder , reviewer "

	_, cmd := m.handleFormKey(namedKey(tea.KeyEnter))
	require.NotNil(t, cmd)

	msg := cmd()
	pa, ok := msg.(projectActionMsg)
	require.True(t, ok)
	require.NoError(t, pa.err)
}

func TestHandleKey_CtrlNOpensForm(t *testing.T) {
	m := New(context.Background(), "127.0.0.1:1")
	m.connected = true
	m.view = viewProjects

	m.handleKey(ctrlKey('n'))
	assert.True(t, m.form.open)
}

func TestHandleKey_CtrlNOpensFormInAnyView(t *testing.T) {
	m := New(context.Background(), "127.0.0.1:1")
	m.connected = true
	m.view = viewProjectDetail

	m.handleKey(ctrlKey('n'))
	assert.True(t, m.form.open, "ctrl+n opens the form from any connected view")
}

func TestHandleKey_FormKeysRoutedToForm(t *testing.T) {
	m := New(context.Background(), "127.0.0.1:1")
	m.connected = true
	m.openForm()

	// Keys should go to the form, not navigation.
	m.handleKey(keyPress("x"))
	assert.Equal(t, "x", m.form.fields[formFieldName])
	assert.False(t, m.pal.open)
}

func TestHandleKey_CtrlSDoesNotPauseInProjectsView(t *testing.T) {
	m := New(context.Background(), "127.0.0.1:1")
	m.connected = true
	m.view = viewProjects
	m.projects = []client.Project{{ID: "p1", Name: "auth", State: "active"}}
	m.cursor = 0

	// ctrl+s in projects view should not trigger pause (it's a detail-view key).
	_, cmd := m.handleKey(ctrlKey('s'))
	assert.Nil(t, cmd)
}

func TestPalette_NewProjectCommand(t *testing.T) {
	m := New(context.Background(), "127.0.0.1:1")
	m.connected = true
	m.openPalette()

	cmds := m.filteredCommands()
	found := false
	for _, c := range cmds {
		if c.label == "New Project" {
			found = true
			break
		}
	}
	assert.True(t, found, "palette should have 'New Project' command")
}

func TestPalette_NoLifecycleCommands(t *testing.T) {
	m := New(context.Background(), "127.0.0.1:1")
	m.connected = true
	m.view = viewProjects
	m.projects = []client.Project{{ID: "p1", Name: "auth", State: "active"}}
	m.cursor = 0

	cmds := m.commands()
	labels := commandLabels(cmds)
	// Lifecycle commands are not in the palette; they are direct keys only.
	assert.NotContains(t, labels, "Pause project")
	assert.NotContains(t, labels, "Resume project")
	assert.NotContains(t, labels, "Finish project")
	assert.NotContains(t, labels, "Assign agent to project…")
	// The five base commands are present.
	assert.Contains(t, labels, "Refresh")
	assert.Contains(t, labels, "Select Cluster")
	assert.Contains(t, labels, "New Project")
	assert.Contains(t, labels, "Switch Project")
	assert.Contains(t, labels, "Quit")
}

func TestPalette_NoLifecycleCommandsOnProjectDetail(t *testing.T) {
	m := New(context.Background(), "127.0.0.1:1")
	m.connected = true
	m.view = viewProjectDetail
	m.selectedProjectID = "p1"
	m.projects = []client.Project{{ID: "p1", Name: "auth", State: "active"}}

	cmds := m.commands()
	labels := commandLabels(cmds)
	// Lifecycle commands are not in the palette even in the detail view.
	assert.NotContains(t, labels, "Pause project")
	assert.NotContains(t, labels, "Finish project")
	assert.NotContains(t, labels, "Assign agent to project…")
}

func TestPalette_NoLifecycleCommandsOnAgentView(t *testing.T) {
	m := New(context.Background(), "127.0.0.1:1")
	m.connected = true
	m.view = viewAgent
	m.selectedProjectID = "p1"
	m.projects = []client.Project{{ID: "p1", Name: "auth", State: "active"}}

	cmds := m.commands()
	labels := commandLabels(cmds)
	assert.NotContains(t, labels, "Pause project")
	assert.NotContains(t, labels, "Assign agent to project…")
}

func TestHandleKey_PauseInProjectDetail(t *testing.T) {
	stub := setupProjectActionServer(t, http.StatusOK, client.Project{ID: "p1", Name: "auth", State: "paused"})
	defer stub.Close()

	m := New(context.Background(), stub.Listener.Addr().String())
	m.connected = true
	m.view = viewProjectDetail
	m.selectedProjectID = "p1"
	m.projects = []client.Project{{ID: "p1", Name: "auth", State: "active"}}

	_, cmd := m.handleKey(ctrlKey('s'))
	require.NotNil(t, cmd)

	msg := cmd()
	pa, ok := msg.(projectActionMsg)
	require.True(t, ok)
	require.NoError(t, pa.err)
	assert.Equal(t, "paused", pa.project.State)
}

func TestHandleKey_ResumeInProjectDetail(t *testing.T) {
	stub := setupProjectActionServer(t, http.StatusOK, client.Project{ID: "p1", Name: "auth", State: "active"})
	defer stub.Close()

	m := New(context.Background(), stub.Listener.Addr().String())
	m.connected = true
	m.view = viewProjectDetail
	m.selectedProjectID = "p1"
	m.projects = []client.Project{{ID: "p1", Name: "auth", State: "paused"}}

	// ctrl+s toggles: paused → resume.
	_, cmd := m.handleKey(ctrlKey('s'))
	require.NotNil(t, cmd)

	msg := cmd()
	pa, ok := msg.(projectActionMsg)
	require.True(t, ok)
	require.NoError(t, pa.err)
	assert.Equal(t, "active", pa.project.State)
}

func TestHandleKey_FinishInProjectDetail(t *testing.T) {
	stub := setupProjectActionServer(t, http.StatusOK, client.Project{ID: "p1", Name: "auth", State: "finished"})
	defer stub.Close()

	m := New(context.Background(), stub.Listener.Addr().String())
	m.connected = true
	m.view = viewProjectDetail
	m.selectedProjectID = "p1"
	m.projects = []client.Project{{ID: "p1", Name: "auth", State: "active"}}

	_, cmd := m.handleKey(ctrlKey('f'))
	require.NotNil(t, cmd)

	msg := cmd()
	pa, ok := msg.(projectActionMsg)
	require.True(t, ok)
	require.NoError(t, pa.err)
	assert.Equal(t, "finished", pa.project.State)
}

func TestHandleKey_AssignInProjectDetail(t *testing.T) {
	stub := setupProjectActionServer(t, http.StatusOK, client.Project{ID: "p1", Name: "auth", State: "active"})
	defer stub.Close()

	m := New(context.Background(), stub.Listener.Addr().String())
	m.connected = true
	m.view = viewProjectDetail
	m.selectedProjectID = "p1"
	m.projects = []client.Project{{ID: "p1", Name: "auth", State: "active", Team: client.ProjectTeam{Agents: []client.TeamAgent{{AgentID: "a1", Name: "greeter"}}}}}
	m.agents = []client.Agent{
		{ID: "a1", Name: "greeter", Status: "running"},
		{ID: "a2", Name: "coder", Status: "running"},
	}

	_, cmd := m.handleKey(ctrlKey('a'))
	require.NotNil(t, cmd)

	msg := cmd()
	pa, ok := msg.(projectActionMsg)
	require.True(t, ok)
	require.NoError(t, pa.err)
}

func TestHandleKey_AssignNoUnassignedAgentReturnsNil(t *testing.T) {
	m := New(context.Background(), "127.0.0.1:1")
	m.connected = true
	m.view = viewProjectDetail
	m.selectedProjectID = "p1"
	m.projects = []client.Project{{ID: "p1", Name: "auth", State: "active", Team: client.ProjectTeam{Agents: []client.TeamAgent{{AgentID: "a1", Name: "greeter"}}}}}
	m.agents = []client.Agent{{ID: "a1", Name: "greeter", Status: "running"}}

	_, cmd := m.handleKey(ctrlKey('a'))
	assert.Nil(t, cmd, "assign should return nil when no unassigned agent")
}

func TestHandleProjectAction_SuccessUpdatesProject(t *testing.T) {
	m := New(context.Background(), "127.0.0.1:1")
	m.connected = true
	m.projects = []client.Project{
		{ID: "p1", Name: "auth", State: "active"},
		{ID: "p2", Name: "billing", State: "paused"},
	}

	// Pause project p1 — should update it in the list.
	m.Update(projectActionMsg{project: client.Project{ID: "p1", Name: "auth", State: "paused"}})
	assert.Equal(t, "paused", m.projects[0].State)
	assert.Equal(t, "paused", m.projects[1].State)
}

func TestHandleProjectAction_NewProjectAppended(t *testing.T) {
	m := New(context.Background(), "127.0.0.1:1")
	m.connected = true
	m.projects = []client.Project{
		{ID: "p1", Name: "auth", State: "active"},
	}

	m.Update(projectActionMsg{project: client.Project{ID: "p2", Name: "billing", State: "active"}})
	require.Len(t, m.projects, 2)
	assert.Equal(t, "p2", m.projects[1].ID)
}

func TestHandleProjectAction_ErrorRefreshesOnly(t *testing.T) {
	m := New(context.Background(), "127.0.0.1:1")
	m.connected = true
	m.projects = []client.Project{{ID: "p1", Name: "auth", State: "active"}}

	_, cmd := m.Update(projectActionMsg{err: assertError("action failed")})
	require.NotNil(t, cmd, "error should trigger a refresh")
	assert.Equal(t, "active", m.projects[0].State, "project state should not change on error")
}

func TestRenderForm_ShowsTitleAndFields(t *testing.T) {
	m := New(context.Background(), "127.0.0.1:1")
	m.openForm()
	m.width, m.height = 80, 24

	out := m.renderForm()
	assert.Contains(t, out, "New project")
	assert.Contains(t, out, "Name")
	assert.Contains(t, out, "Workspace")
	assert.Contains(t, out, "Goal")
	assert.Contains(t, out, "Agents")
	assert.Contains(t, out, "enter create")
	assert.Contains(t, out, "esc cancel")
}

func TestRenderForm_ShowsFieldValues(t *testing.T) {
	m := New(context.Background(), "127.0.0.1:1")
	m.openForm()
	m.form.fields[formFieldName] = "auth-service"
	m.form.fields[formFieldWorkspace] = "~/work/auth"

	out := m.renderForm()
	assert.Contains(t, out, "auth-service")
	assert.Contains(t, out, "~/work/auth")
}

func TestRenderForm_EmptyFieldsShowPlaceholder(t *testing.T) {
	m := New(context.Background(), "127.0.0.1:1")
	m.openForm()
	m.form.cursor = formFieldWorkspace // focus on workspace, name is empty

	out := m.renderForm()
	// The Name field should show (empty) placeholder since it's not focused.
	assert.Contains(t, out, "(empty)")
}

func TestView_FormOverlayWhenOpen(t *testing.T) {
	m := New(context.Background(), "127.0.0.1:1")
	m.connected = true
	m.width, m.height = 80, 24

	assert.NotContains(t, m.View().Content, "New project")

	m.openForm()
	content := m.View().Content
	assert.Contains(t, content, "New project")
}

func TestView_FormOverPalettePrecedence(t *testing.T) {
	m := New(context.Background(), "127.0.0.1:1")
	m.connected = true
	m.width, m.height = 80, 24

	// If both are somehow open, palette takes precedence in renderOverlay.
	m.openPalette()
	m.openForm()
	content := m.View().Content
	// Palette renders "Commands", form renders "New project".
	// palette.open is checked first in renderOverlay.
	assert.Contains(t, content, "Commands")
}

func TestSelectedProjectIDForAction_ProjectsViewUsesCursor(t *testing.T) {
	m := New(context.Background(), "127.0.0.1:1")
	m.connected = true
	m.view = viewProjects
	m.projects = []client.Project{
		{ID: "p1", Name: "auth", State: "active"},
		{ID: "p2", Name: "billing", State: "paused"},
	}
	m.cursor = 1

	assert.Equal(t, "p2", m.selectedProjectIDForAction())
}

func TestSelectedProjectIDForAction_DetailViewUsesSelectedID(t *testing.T) {
	m := New(context.Background(), "127.0.0.1:1")
	m.connected = true
	m.view = viewProjectDetail
	m.selectedProjectID = "p2"
	m.projects = []client.Project{
		{ID: "p1", Name: "auth", State: "active"},
		{ID: "p2", Name: "billing", State: "paused"},
	}

	assert.Equal(t, "p2", m.selectedProjectIDForAction())
}

func TestSelectedProjectIDForAction_EmptyWhenNoProject(t *testing.T) {
	m := New(context.Background(), "127.0.0.1:1")
	m.connected = true
	m.view = viewProjects
	m.projects = nil

	assert.Empty(t, m.selectedProjectIDForAction())
}

func TestFirstUnassignedAgentName(t *testing.T) {
	m := New(context.Background(), "127.0.0.1:1")
	m.projects = []client.Project{{
		ID: "p1", Name: "auth", State: "active",
		Team: client.ProjectTeam{Agents: []client.TeamAgent{
			{AgentID: "a1", Name: "greeter"},
		}},
	}}
	m.agents = []client.Agent{
		{ID: "a1", Name: "greeter", Status: "running"},
		{ID: "a2", Name: "coder", Status: "running"},
	}

	assert.Equal(t, "coder", m.firstUnassignedAgentName("p1"))
}

func TestFirstUnassignedAgentName_NoneAvailable(t *testing.T) {
	m := New(context.Background(), "127.0.0.1:1")
	m.projects = []client.Project{{
		ID: "p1", Name: "auth", State: "active",
		Team: client.ProjectTeam{Agents: []client.TeamAgent{
			{AgentID: "a1", Name: "greeter"},
		}},
	}}
	m.agents = []client.Agent{{ID: "a1", Name: "greeter", Status: "running"}}

	assert.Empty(t, m.firstUnassignedAgentName("p1"))
}

func TestActionProjectState(t *testing.T) {
	m := New(context.Background(), "127.0.0.1:1")
	m.projects = []client.Project{
		{ID: "p1", Name: "auth", State: "active"},
		{ID: "p2", Name: "billing", State: "paused"},
	}

	assert.Equal(t, "active", m.actionProjectState("p1"))
	assert.Equal(t, "paused", m.actionProjectState("p2"))
	assert.Empty(t, m.actionProjectState("p3"))
}

func TestDialogOffset_CentersDialog(t *testing.T) {
	m := New(context.Background(), "127.0.0.1:1")
	m.width, m.height = 80, 24

	dialog := "short"
	x, y := m.dialogOffset(dialog)
	assert.GreaterOrEqual(t, x, 0)
	assert.GreaterOrEqual(t, y, 0)
}

// --- helpers ---

func commandLabels(cmds []command) []string {
	out := make([]string, len(cmds))
	for i, c := range cmds {
		out[i] = c.label
	}
	return out
}

type assertError string

func (e assertError) Error() string { return string(e) }

// setupProjectActionServer returns a test server that handles project
// lifecycle endpoints (pause/resume/finish/assign/create) and returns the
// given status code + project on success.
func setupProjectActionServer(t *testing.T, status int, response client.Project) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	mux.HandleFunc("/api/v1/health", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})
	mux.HandleFunc("/api/v1/node", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"mode": "master", "leader_connected": true, "node_id": "n1"})
	})
	mux.HandleFunc("/api/v1/agents", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode([]client.Agent{})
	})
	mux.HandleFunc("/api/v1/agents/context", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode([]any{})
	})
	// Catch-all for projects: handles GET (list), POST (create), and lifecycle
	// sub-paths (pause/resume/finish/agents) by returning the canned response
	// for POST and an empty list for GET.
	mux.HandleFunc("/api/v1/projects/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(response)
			return
		}
		_ = json.NewEncoder(w).Encode([]client.Project{})
	})

	return httptest.NewServer(mux)
}

func TestRenderForm_HasRoundedBorder(t *testing.T) {
	m := New(context.Background(), "127.0.0.1:1")
	m.openForm()
	m.width, m.height = 80, 24

	out := m.renderForm()
	// Rounded border uses ╭ and ╯ corner characters.
	assert.Contains(t, out, "╭")
	assert.True(t, strings.Contains(out, "╯"), "form should have rounded border bottom-right corner")
}
