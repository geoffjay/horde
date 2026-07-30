package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/geoffjay/horde/internal/client"
)

// navTestHandler returns an http.Handler with a small set of projects,
// agents, and execution contexts so the navigation flow has data to render
// and drill into.
func navTestHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/health", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})
	mux.HandleFunc("/api/v1/node", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"mode": "coordinator", "leader_connected": true, "node_id": "n1", "version": "test",
		})
	})
	mux.HandleFunc("/api/v1/agents", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode([]client.Agent{
			{ID: "a1", Name: "greeter", Status: "running"},
			{ID: "a2", Name: "coder", Status: "running"},
		})
	})
	mux.HandleFunc("/api/v1/agents/context", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode([]client.ExecutionContext{
			{AgentID: "a1", Activity: client.StateIdle, Lifecycle: client.AgentRunning},
			{AgentID: "a2", Activity: client.StateBusy, Lifecycle: client.AgentRunning},
		})
	})
	mux.HandleFunc("/api/v1/projects/", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode([]client.Project{
			{ID: "p1", Name: "auth-service", State: "active", Goal: "Fix login"},
			{ID: "p2", Name: "billing", State: "paused", Goal: "Migrate to Stripe"},
		})
	})
	return mux
}

// connectedNavModel returns a model backed by navTestHandler, loaded and marked
// connected, ready for navigation tests. The caller must close the returned
// server.
func connectedNavModel(t *testing.T) (*Model, *httptest.Server) {
	t.Helper()
	stub := httptest.NewServer(navTestHandler())
	m := newTestModel(stub.Listener.Addr().String())
	m.Update(m.loadNode())
	m.connected = true
	return m, stub
}

// selectProjectChild expands the Projects group, moves the sidebar cursor to the
// child with the given id, and selects it (as pressing enter on that row would).
func selectProjectChild(m *Model, id string) {
	m.jumpToChild(groupProjects, id)
}

func TestNew_DefaultsToProjectsView(t *testing.T) {
	m := newTestModel("127.0.0.1:1")
	assert.Equal(t, viewProjects, m.view)
	assert.Equal(t, focusSidebar, m.focus)
	// The sidebar cursor starts on the Projects group header.
	row, ok := m.currentSidebarRow()
	require.True(t, ok)
	assert.Equal(t, rowGroup, row.kind)
	assert.Equal(t, groupProjects, row.group)
}

func TestLoadNode_PopulatesProjects(t *testing.T) {
	stub := httptest.NewServer(navTestHandler())
	defer stub.Close()

	m := newTestModel(stub.Listener.Addr().String())
	msg := m.loadNode()
	nm, ok := msg.(nodeInfoMsg)
	require.True(t, ok)
	require.NoError(t, nm.err)
	assert.Len(t, nm.projects, 2)
	assert.Equal(t, "auth-service", nm.projects[0].Name)

	m.Update(msg)
	assert.Len(t, m.projects, 2)
}

func TestSidebarSelect_ProjectOpensDetail(t *testing.T) {
	m, stub := connectedNavModel(t)
	defer stub.Close()

	// Enter on the Projects group header expands it and shows the overview,
	// keeping focus on the sidebar.
	require.Equal(t, groupProjects, mustRow(t, m).group)
	m.Update(namedKey(tea.KeyEnter))
	assert.Equal(t, viewProjects, m.view)
	assert.Equal(t, focusSidebar, m.focus)
	assert.True(t, m.sidebar.expanded[groupProjects])

	// Down to the first project child, enter selects it and hands focus to the
	// detail pane.
	m.Update(namedKey(tea.KeyDown))
	m.Update(namedKey(tea.KeyEnter))
	assert.Equal(t, viewProjectDetail, m.view)
	assert.Equal(t, focusDetail, m.focus)
	assert.Equal(t, "p1", m.selectedProjectID)
}

func TestDetailEsc_ReturnsToSidebar(t *testing.T) {
	m, stub := connectedNavModel(t)
	defer stub.Close()

	selectProjectChild(m, "p1")
	require.Equal(t, focusDetail, m.focus)
	require.Equal(t, viewProjectDetail, m.view)

	// esc with an empty detail drill returns focus to the sidebar; the view
	// stays put (the sidebar still points at this project).
	m.Update(escKey())
	assert.Equal(t, focusSidebar, m.focus)
	assert.Equal(t, viewProjectDetail, m.view)
}

func TestDetailDrill_ProjectToAgentAndBack(t *testing.T) {
	m, stub := connectedNavModel(t)
	defer stub.Close()

	// Give the project a team agent so the roster has something to drill into.
	m.projects[0].Team.Agents = []client.TeamAgent{{AgentID: "a1", Name: "greeter"}}
	selectProjectChild(m, "p1")
	require.Equal(t, viewProjectDetail, m.view)

	// enter drills into the team agent's detail.
	m.Update(namedKey(tea.KeyEnter))
	assert.Equal(t, viewAgent, m.view)
	assert.Equal(t, "a1", m.selectedAgentID)
	require.Len(t, m.detailBack, 1)

	// esc pops back to the project roster.
	m.Update(escKey())
	assert.Equal(t, viewProjectDetail, m.view)
	assert.Empty(t, m.detailBack)
	assert.Equal(t, focusDetail, m.focus)
}

func TestDetailCursor_ClampsToProjects(t *testing.T) {
	m, stub := connectedNavModel(t)
	defer stub.Close()

	// Focus the detail pane on the projects overview so up/down drive the
	// detail list cursor.
	m.view = viewProjects
	m.focus = focusDetail
	require.Len(t, m.projects, 2)
	assert.Equal(t, 0, m.cursor)

	m.Update(namedKey(tea.KeyDown))
	assert.Equal(t, 1, m.cursor)
	m.Update(namedKey(tea.KeyDown))
	assert.Equal(t, 1, m.cursor, "cursor clamps to the last project")
	m.Update(namedKey(tea.KeyUp))
	assert.Equal(t, 0, m.cursor)
	m.Update(namedKey(tea.KeyUp))
	assert.Equal(t, 0, m.cursor, "cursor clamps at the top")
}

func TestGoCluster_SelectsNodesGroup(t *testing.T) {
	m := newTestModel("127.0.0.1:1")
	m.goCluster()
	assert.Equal(t, viewCluster, m.view)
	assert.True(t, m.sidebar.expanded[groupNodes])
	assert.Equal(t, focusSidebar, m.focus)
}

func TestRenderView_DispatchesByView(t *testing.T) {
	m, stub := connectedNavModel(t)
	defer stub.Close()
	m.width, m.height = 80, 24

	// Projects view renders project names.
	out := m.renderView()
	assert.Contains(t, out, "auth-service")

	// Cluster view renders node info.
	m.goCluster()
	out = m.renderView()
	assert.Contains(t, out, "leader")
}

func TestListLength_ByView(t *testing.T) {
	m, stub := connectedNavModel(t)
	defer stub.Close()

	// Projects view: 2 projects.
	assert.Equal(t, 2, m.listLength())

	// Cluster view: 0 registered workers.
	m.goCluster()
	assert.Equal(t, 0, m.listLength())
}

func TestPaletteStillWorks_NavigationKeysAreIgnored(t *testing.T) {
	m, stub := connectedNavModel(t)
	defer stub.Close()

	m.openPalette()
	require.True(t, m.pal.open)

	m.Update(namedKey(tea.KeyEnter))
	// The palette consumed the enter; if it stayed open the view must not have
	// drilled in.
	if m.pal.open {
		assert.Equal(t, viewProjects, m.view, "drill-in should not fire while palette is open")
	}
}

func TestHandleKey_DisconnectedIgnoresNavigation(t *testing.T) {
	m := newTestModel("127.0.0.1:1")
	m.connected = false
	startCursor := m.sidebar.cursor

	m.Update(namedKey(tea.KeyDown))
	assert.Equal(t, startCursor, m.sidebar.cursor, "sidebar cursor should not move while disconnected")
	m.Update(namedKey(tea.KeyEnter))
	assert.Equal(t, viewProjects, m.view)
}

// mustRow returns the current sidebar row, failing the test if there is none.
func mustRow(t *testing.T, m *Model) sidebarRow {
	t.Helper()
	row, ok := m.currentSidebarRow()
	require.True(t, ok)
	return row
}
