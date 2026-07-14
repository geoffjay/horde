package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPersistentProjectStore_FlushAndLoad(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "projects.json")

	// Create a store, add a project, and verify it flushes to disk.
	ps := newPersistentProjectStore(path)
	_, err := ps.Create(CreateProjectInput{
		Name:       "test-proj",
		Workspace:  "/tmp/work",
		Goal:       "fix bugs",
		AgentNames: []string{"greeter"},
	})
	require.NoError(t, err)

	// The file should exist on disk.
	assert.FileExists(t, path)

	// Read it back and verify the structure.
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var state projectStoreState
	require.NoError(t, json.Unmarshal(data, &state))
	assert.Len(t, state.Projects, 1)
	assert.Equal(t, "test-proj", state.Projects[0].Name)
	assert.Equal(t, "active", string(state.Projects[0].State))
	assert.Equal(t, 1, state.NextID)

	// Create a second store pointing at the same file and load.
	ps2 := newPersistentProjectStore(path)
	mem := ps2.(*memProjectStore)
	require.NoError(t, mem.loadProjects())

	// The loaded store should have the project.
	p, err := ps2.Get(state.Projects[0].ID)
	require.NoError(t, err)
	assert.Equal(t, "test-proj", p.Name)
	assert.Equal(t, "fix bugs", p.Goal)
	assert.Len(t, p.Team.Agents, 1)

	// The nextID counter should be preserved so new projects don't collide.
	_, err = ps2.Create(CreateProjectInput{
		Name:       "second-proj",
		AgentNames: []string{"repeater"},
	})
	require.NoError(t, err)
	projects := ps2.List("")
	assert.Len(t, projects, 2)
}

func TestPersistentProjectStore_LoadMissingFile(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "nonexistent", "projects.json")

	ps := newPersistentProjectStore(path)
	mem := ps.(*memProjectStore)

	// Loading from a nonexistent file is not an error — it's a fresh start.
	err := mem.loadProjects()
	require.NoError(t, err)
	assert.Empty(t, ps.List(""))
}

func TestPersistentProjectStore_MutationsFlush(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "projects.json")

	ps := newPersistentProjectStore(path)

	// Create.
	p, err := ps.Create(CreateProjectInput{
		Name:       "p1",
		AgentNames: []string{"greeter"},
	})
	require.NoError(t, err)
	assert.FileExists(t, path)

	var state projectStoreState

	// Assign agent (while still active).
	_, err = ps.AssignAgent(p.ID, "a1", "repeater")
	require.NoError(t, err)
	data, _ := os.ReadFile(path)
	require.NoError(t, json.Unmarshal(data, &state))
	assert.Len(t, state.Projects[0].Team.Agents, 2)

	// Remove agent.
	_, err = ps.RemoveAgent(p.ID, "a1")
	require.NoError(t, err)
	data, _ = os.ReadFile(path)
	require.NoError(t, json.Unmarshal(data, &state))
	assert.Len(t, state.Projects[0].Team.Agents, 1)

	// Update state (after agent operations).
	_, err = ps.UpdateState(p.ID, ProjectPaused)
	require.NoError(t, err)
	data, _ = os.ReadFile(path)
	require.NoError(t, json.Unmarshal(data, &state))
	assert.Equal(t, "paused", string(state.Projects[0].State))

	// Delete.
	err = ps.Delete(p.ID)
	require.NoError(t, err)
	data, _ = os.ReadFile(path)
	require.NoError(t, json.Unmarshal(data, &state))
	assert.Empty(t, state.Projects)
}

func TestPersistentProjectStore_InMemoryOnlyNoFile(t *testing.T) {
	// A store created with newProjectStore (no path) should never touch disk.
	ps := newProjectStore()
	_, err := ps.Create(CreateProjectInput{
		Name:       "in-mem",
		AgentNames: []string{"greeter"},
	})
	require.NoError(t, err)
	// No file should be created (path is empty).
	assert.Equal(t, "", ps.(*memProjectStore).path)
}

func TestPersistentProjectStore_RoundTripWithAssign(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "projects.json")

	// Create, assign an agent, then load and verify the team is preserved.
	ps1 := newPersistentProjectStore(path)
	p, err := ps1.Create(CreateProjectInput{
		Name:       "round-trip",
		AgentNames: []string{"greeter"},
	})
	require.NoError(t, err)

	_, err = ps1.AssignAgent(p.ID, "a1", "repeater")
	require.NoError(t, err)

	// Load into a new store.
	ps2 := newPersistentProjectStore(path)
	require.NoError(t, ps2.(*memProjectStore).loadProjects())

	loaded, err := ps2.Get(p.ID)
	require.NoError(t, err)
	assert.Len(t, loaded.Team.Agents, 2)
	assert.Equal(t, "greeter", loaded.Team.Agents[0].Name)
	assert.Equal(t, "repeater", loaded.Team.Agents[1].Name)
	assert.Equal(t, "a1", loaded.Team.Agents[1].AgentID)
}
