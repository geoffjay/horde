package server

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAttachAgent(t *testing.T) {
	srv, err := New(Config{SpawnDefaultAgent: false})
	require.NoError(t, err)

	// Create the project directly in the store (an unspawned team slot) so the
	// test needs no agent subprocess.
	p, err := srv.projects.Create(CreateProjectInput{Name: "auth", AgentNames: []string{"greeter"}})
	require.NoError(t, err)

	// Unknown project and unknown agent id both error.
	_, err = srv.AttachAgent("ghost-project", "a1")
	require.ErrorIs(t, err, ErrProjectNotFound)
	_, err = srv.AttachAgent(p.ID, "ghost-agent")
	require.ErrorIs(t, err, ErrAgentNotFound)

	// Register a running agent (as if spawned standalone), then attach it.
	srv.mu.Lock()
	srv.procs["a1"] = &agentProc{id: "a1", name: "greeter", state: AgentRunning}
	srv.mu.Unlock()
	srv.ctxStore.init("a1", srv.cfg.NodeID)

	updated, err := srv.AttachAgent(p.ID, "a1")
	require.NoError(t, err)

	found := false
	for _, ta := range updated.Team.Agents {
		if ta.AgentID == "a1" && ta.Name == "greeter" {
			found = true
		}
	}
	assert.True(t, found, "the attached agent is on the team")
	assert.Equal(t, p.ID, srv.AgentActiveProject("a1"), "attach binds the active project")

	// Attaching again is idempotent (no duplicate team entry).
	updated, err = srv.AttachAgent(p.ID, "a1")
	require.NoError(t, err)
	count := 0
	for _, ta := range updated.Team.Agents {
		if ta.AgentID == "a1" {
			count++
		}
	}
	assert.Equal(t, 1, count, "attach is idempotent")
}
