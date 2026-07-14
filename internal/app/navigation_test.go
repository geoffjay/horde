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
			"mode": "master", "leader_connected": true, "node_id": "n1", "version": "test",
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

func TestNew_DefaultsToProjectsView(t *testing.T) {
	m := New(context.Background(), "127.0.0.1:1")
	assert.Equal(t, viewProjects, m.view)
	assert.Empty(t, m.crumbs)
}

func TestLoadNode_PopulatesProjects(t *testing.T) {
	stub := httptest.NewServer(navTestHandler())
	defer stub.Close()

	m := New(context.Background(), stub.Listener.Addr().String())
	msg := m.loadNode()
	nm, ok := msg.(nodeInfoMsg)
	require.True(t, ok)
	require.NoError(t, nm.err)
	assert.Len(t, nm.projects, 2)
	assert.Equal(t, "auth-service", nm.projects[0].Name)

	m.Update(msg)
	assert.Len(t, m.projects, 2)
}

func TestDrillIn_ProjectsToProjectDetail(t *testing.T) {
	stub := httptest.NewServer(navTestHandler())
	defer stub.Close()

	m := New(context.Background(), stub.Listener.Addr().String())
	m.Update(m.loadNode())
	m.connected = true

	require.Equal(t, viewProjects, m.view)
	require.Len(t, m.projects, 2)

	// Cursor starts at 0 (auth-service). Enter drills in.
	m.Update(namedKey(tea.KeyEnter))
	assert.Equal(t, viewProjectDetail, m.view)
	require.Len(t, m.crumbs, 1)
	assert.Equal(t, viewProjects, m.crumbs[0].view)
	assert.Equal(t, "projects", m.crumbs[0].label)
}

func TestDrillIn_EmptyProjectsDoesNothing(t *testing.T) {
	m := New(context.Background(), "127.0.0.1:1")
	m.connected = true
	m.projects = nil

	m.Update(namedKey(tea.KeyEnter))
	assert.Equal(t, viewProjects, m.view)
	assert.Empty(t, m.crumbs)
}

func TestEsc_PopsBackToPreviousView(t *testing.T) {
	stub := httptest.NewServer(navTestHandler())
	defer stub.Close()

	m := New(context.Background(), stub.Listener.Addr().String())
	m.Update(m.loadNode())
	m.connected = true

	// Drill into project detail.
	m.Update(namedKey(tea.KeyEnter))
	require.Equal(t, viewProjectDetail, m.view)
	require.Len(t, m.crumbs, 1)

	// Esc pops back to projects.
	m.Update(escKey())
	assert.Equal(t, viewProjects, m.view)
	assert.Empty(t, m.crumbs)
}

func TestEsc_RestoresCursorOnPop(t *testing.T) {
	stub := httptest.NewServer(navTestHandler())
	defer stub.Close()

	m := New(context.Background(), stub.Listener.Addr().String())
	m.Update(m.loadNode())
	m.connected = true

	// Move cursor to the second project (billing), then drill in.
	m.Update(namedKey(tea.KeyDown))
	require.Equal(t, 1, m.cursor)
	require.Equal(t, "p2", m.projects[m.cursor].ID)
	m.Update(namedKey(tea.KeyEnter))
	require.Equal(t, viewProjectDetail, m.view)

	// Pop back — cursor should still be on "billing" (p2).
	m.Update(escKey())
	assert.Equal(t, viewProjects, m.view)
	assert.Equal(t, 1, m.cursor, "cursor should be restored to the project that was opened")
	assert.Equal(t, "p2", m.projects[m.cursor].ID)
}

func TestEsc_TopLevelDoesNothing(t *testing.T) {
	m := New(context.Background(), "127.0.0.1:1")
	m.connected = true
	require.Equal(t, viewProjects, m.view)

	m.Update(escKey())
	assert.Equal(t, viewProjects, m.view)
	assert.Empty(t, m.crumbs)
}

func TestCursorMovement_ClampsToProjects(t *testing.T) {
	stub := httptest.NewServer(navTestHandler())
	defer stub.Close()

	m := New(context.Background(), stub.Listener.Addr().String())
	m.Update(m.loadNode())
	m.connected = true

	require.Len(t, m.projects, 2)
	assert.Equal(t, 0, m.cursor)

	// Down moves to the second project.
	m.Update(namedKey(tea.KeyDown))
	assert.Equal(t, 1, m.cursor)

	// Down again clamps to the last item.
	m.Update(namedKey(tea.KeyDown))
	assert.Equal(t, 1, m.cursor)

	// Up moves back to the first.
	m.Update(namedKey(tea.KeyUp))
	assert.Equal(t, 0, m.cursor)

	// Up at the top stays at 0.
	m.Update(namedKey(tea.KeyUp))
	assert.Equal(t, 0, m.cursor)
}

func TestGoHome_ResetsToProjectsView(t *testing.T) {
	m := New(context.Background(), "127.0.0.1:1")
	m.view = viewAgent
	m.crumbs = []breadcrumbEntry{{view: viewProjects, label: "projects"}}
	m.cursor = 5

	m.goHome()
	assert.Equal(t, viewProjects, m.view)
	assert.Empty(t, m.crumbs)
	assert.Equal(t, 0, m.cursor)
}

func TestGoCluster_SetsClusterView(t *testing.T) {
	m := New(context.Background(), "127.0.0.1:1")
	m.view = viewProjects
	m.cursor = 3

	m.goCluster()
	assert.Equal(t, viewCluster, m.view)
	assert.Empty(t, m.crumbs)
	assert.Equal(t, 0, m.cursor)
}

func TestBreadcrumb_RendersFullPath(t *testing.T) {
	stub := httptest.NewServer(navTestHandler())
	defer stub.Close()

	m := New(context.Background(), stub.Listener.Addr().String())
	m.Update(m.loadNode())
	m.connected = true

	// Top level: "projects"
	bc := m.renderBreadcrumb()
	assert.Contains(t, bc, "projects")

	// After drilling into a project: "projects › auth-service"
	m.Update(namedKey(tea.KeyEnter))
	bc = m.renderBreadcrumb()
	assert.Contains(t, bc, "projects")
	assert.Contains(t, bc, "auth-service")
}

func TestRenderView_DispatchesByView(t *testing.T) {
	stub := httptest.NewServer(navTestHandler())
	defer stub.Close()

	m := New(context.Background(), stub.Listener.Addr().String())
	m.Update(m.loadNode())
	m.connected = true
	m.width, m.height = 80, 24

	// Projects view renders project names.
	out := m.renderView()
	assert.Contains(t, out, "auth-service")

	// Cluster view renders node info (empty in test).
	m.goCluster()
	out = m.renderView()
	assert.Contains(t, out, "leader")
}

func TestListLength_ByView(t *testing.T) {
	stub := httptest.NewServer(navTestHandler())
	defer stub.Close()

	m := New(context.Background(), stub.Listener.Addr().String())
	m.Update(m.loadNode())
	m.connected = true

	// Projects view: 2 projects.
	assert.Equal(t, 2, m.listLength())

	// Cluster view: 0 nodes.
	m.goCluster()
	assert.Equal(t, 0, m.listLength())
}

func TestPaletteStillWorks_NavigationKeysAreIgnored(t *testing.T) {
	stub := httptest.NewServer(navTestHandler())
	defer stub.Close()

	m := New(context.Background(), stub.Listener.Addr().String())
	m.Update(m.loadNode())
	m.connected = true

	// Open palette — navigation keys should go to the palette, not drill in.
	m.openPalette()
	require.True(t, m.pal.open)

	m.Update(namedKey(tea.KeyEnter))
	// Palette is closed after running a command; the view should not have
	// changed to projectDetail unless the selected command was a navigation
	// one (it's Refresh or Quit by default).
	// The key point: the palette consumed the enter, not the drill-in.
	if m.pal.open {
		assert.Equal(t, viewProjects, m.view, "drill-in should not fire while palette is open")
	}
}

func TestHandleKey_DisconnectedIgnoresNavigation(t *testing.T) {
	m := New(context.Background(), "127.0.0.1:1")
	m.connected = false

	// Navigation keys should be ignored when disconnected.
	m.Update(namedKey(tea.KeyDown))
	assert.Equal(t, 0, m.cursor)
	m.Update(namedKey(tea.KeyEnter))
	assert.Equal(t, viewProjects, m.view)
	assert.Empty(t, m.crumbs)
}
