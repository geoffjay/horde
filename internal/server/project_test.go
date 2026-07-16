package server

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Compile-time: memProjectStore satisfies ProjectStore.
var _ ProjectStore = (*memProjectStore)(nil)

func TestNewProjectStore_ReturnsInterface(t *testing.T) {
	ps := newProjectStore()
	assert.NotNil(t, ps)
	// The concrete type should be *memProjectStore.
	_, ok := ps.(*memProjectStore)
	assert.True(t, ok, "newProjectStore should return *memProjectStore")
}

func TestProjectStore_Create(t *testing.T) {
	ps := newProjectStore()

	p, err := ps.Create(CreateProjectInput{
		Name:       "my-project",
		Workspace:  "/tmp/work",
		Goal:       "fix bugs",
		AgentNames: []string{"greeter"},
	})
	require.NoError(t, err)
	assert.Equal(t, "my-project", p.Name)
	assert.Equal(t, "/tmp/work", p.Workspace)
	assert.Equal(t, "fix bugs", p.Goal)
	assert.Equal(t, ProjectActive, p.State)
	assert.Len(t, p.Team.Agents, 1)
	assert.Equal(t, "greeter", p.Team.Agents[0].Name)
	assert.NotEmpty(t, p.ID)
}

func TestProjectStore_Create_RequiresName(t *testing.T) {
	ps := newProjectStore()
	_, err := ps.Create(CreateProjectInput{AgentNames: []string{"greeter"}})
	require.Error(t, err)
}

func TestProjectStore_Create_RequiresAgents(t *testing.T) {
	ps := newProjectStore()
	_, err := ps.Create(CreateProjectInput{Name: "p"})
	require.Error(t, err)
}

func TestProjectStore_Get(t *testing.T) {
	ps := newProjectStore()
	p, _ := ps.Create(CreateProjectInput{Name: "p", AgentNames: []string{"greeter"}})

	got, err := ps.Get(p.ID)
	require.NoError(t, err)
	assert.Equal(t, p.ID, got.ID)
	assert.Equal(t, p.Name, got.Name)
}

func TestProjectStore_Get_NotFound(t *testing.T) {
	ps := newProjectStore()
	_, err := ps.Get("nope")
	assert.ErrorIs(t, err, ErrProjectNotFound)
}

func TestProjectStore_List(t *testing.T) {
	ps := newProjectStore()
	_, _ = ps.Create(CreateProjectInput{Name: "a", AgentNames: []string{"greeter"}})
	_, _ = ps.Create(CreateProjectInput{Name: "b", AgentNames: []string{"greeter"}})

	all := ps.List("")
	assert.Len(t, all, 2)
}

func TestProjectStore_List_FilterByState(t *testing.T) {
	ps := newProjectStore()
	p1, _ := ps.Create(CreateProjectInput{Name: "a", AgentNames: []string{"greeter"}})
	_, _ = ps.Create(CreateProjectInput{Name: "b", AgentNames: []string{"greeter"}})
	_, _ = ps.UpdateState(p1.ID, ProjectPaused)

	active := ps.List(ProjectActive)
	assert.Len(t, active, 1)
	assert.Equal(t, "b", active[0].Name)

	paused := ps.List(ProjectPaused)
	assert.Len(t, paused, 1)
	assert.Equal(t, "a", paused[0].Name)
}

func TestProjectStore_UpdateState(t *testing.T) {
	ps := newProjectStore()
	p, _ := ps.Create(CreateProjectInput{Name: "p", AgentNames: []string{"greeter"}})

	updated, err := ps.UpdateState(p.ID, ProjectPaused)
	require.NoError(t, err)
	assert.Equal(t, ProjectPaused, updated.State)

	_, err = ps.UpdateState(p.ID, ProjectFinished)
	require.NoError(t, err)
	got, _ := ps.Get(p.ID)
	assert.Equal(t, ProjectFinished, got.State)
}

func TestProjectStore_UpdateState_NotFound(t *testing.T) {
	ps := newProjectStore()
	_, err := ps.UpdateState("nope", ProjectPaused)
	assert.ErrorIs(t, err, ErrProjectNotFound)
}

func TestProjectStore_AssignAgent(t *testing.T) {
	ps := newProjectStore()
	p, _ := ps.Create(CreateProjectInput{Name: "p", AgentNames: []string{"greeter"}})

	updated, err := ps.AssignAgent(p.ID, "a1", "repeater")
	require.NoError(t, err)
	assert.Len(t, updated.Team.Agents, 2)
}

func TestProjectStore_AssignAgent_Idempotent(t *testing.T) {
	ps := newProjectStore()
	p, _ := ps.Create(CreateProjectInput{Name: "p", AgentNames: []string{"greeter"}})

	// First assignment adds the agent.
	_, err := ps.AssignAgent(p.ID, "a1", "repeater")
	require.NoError(t, err)

	// Second assignment to the same agent id is a no-op.
	updated, err := ps.AssignAgent(p.ID, "a1", "repeater")
	require.NoError(t, err)
	assert.Len(t, updated.Team.Agents, 2)
}

func TestProjectStore_AssignAgent_NotActive(t *testing.T) {
	ps := newProjectStore()
	p, _ := ps.Create(CreateProjectInput{Name: "p", AgentNames: []string{"greeter"}})
	_, _ = ps.UpdateState(p.ID, ProjectPaused)

	_, err := ps.AssignAgent(p.ID, "a1", "repeater")
	assert.ErrorIs(t, err, ErrProjectNotActive)
}

func TestProjectStore_RemoveAgent(t *testing.T) {
	ps := newProjectStore()
	p, _ := ps.Create(CreateProjectInput{Name: "p", AgentNames: []string{"greeter"}})
	_, _ = ps.AssignAgent(p.ID, "a1", "repeater")

	updated, err := ps.RemoveAgent(p.ID, "a1")
	require.NoError(t, err)
	assert.Len(t, updated.Team.Agents, 1)
	assert.Equal(t, "greeter", updated.Team.Agents[0].Name)
}

func TestProjectStore_Delete(t *testing.T) {
	ps := newProjectStore()
	p, _ := ps.Create(CreateProjectInput{Name: "p", AgentNames: []string{"greeter"}})

	err := ps.Delete(p.ID)
	require.NoError(t, err)
	_, err = ps.Get(p.ID)
	assert.ErrorIs(t, err, ErrProjectNotFound)
}

func TestServer_SessionKey_NoProject(t *testing.T) {
	srv, err := New(Config{SpawnDefaultAgent: false})
	require.NoError(t, err)
	require.NoError(t, srv.Start(context.Background()))
	t.Cleanup(func() {
		for _, a := range srv.Agents() {
			_ = srv.StopAgent(a.ID)
		}
	})

	// An agent with no active project returns an empty session key.
	assert.Equal(t, "", srv.SessionKey("nonexistent"))
}

func TestServer_CreateProject_SpawnFailureRollsBack(t *testing.T) {
	srv, err := New(Config{Mode: ModeMaster, SpawnDefaultAgent: false})
	require.NoError(t, err)

	// "ghost" is not a registered agent, so it fails to spawn. Creation must
	// roll back atomically rather than leave a project with an unspawnable
	// team entry (whose empty agent id would later fail invoke with a 404).
	_, err = srv.CreateProject(context.Background(), CreateProjectInput{
		Name:       "doomed",
		AgentNames: []string{"ghost"},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ghost")
	assert.Contains(t, err.Error(), "failed to spawn")
	assert.Empty(t, srv.ListProjects(""), "a failed create must not leave a project behind")
}

func TestServer_AgentActiveProject(t *testing.T) {
	srv, err := New(Config{SpawnDefaultAgent: false})
	require.NoError(t, err)
	require.NoError(t, srv.Start(context.Background()))
	t.Cleanup(func() {
		for _, a := range srv.Agents() {
			_ = srv.StopAgent(a.ID)
		}
	})

	// No active project for an unknown agent.
	assert.Equal(t, "", srv.AgentActiveProject("nonexistent"))
}
