package app

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/geoffjay/horde/internal/client"
)

func TestAgentsView_ListLengthAndSelection(t *testing.T) {
	m := New(context.Background(), "127.0.0.1:1")
	m.view = viewAgents
	m.agents = []client.Agent{
		{ID: "a1", Name: "greeter", Status: "running"},
		{ID: "a2", Name: "coder", Status: "running"},
	}

	assert.Equal(t, 2, m.listLength())

	// The cursor selects by index in the Agents list.
	m.cursor = 1
	a, ok := m.selectedAgent()
	require.True(t, ok)
	assert.Equal(t, "a2", a.ID)

	// A pinned selectedAgentID wins over the cursor (standalone invoke path).
	m.selectedAgentID = "a1"
	a, ok = m.selectedAgent()
	require.True(t, ok)
	assert.Equal(t, "a1", a.ID)
}

func TestGoAgents_ClearsSelectionAndEntersView(t *testing.T) {
	m := New(context.Background(), "127.0.0.1:1")
	m.selectedAgentID = "stale"
	m.selectedProjectID = "p1"

	m.goAgents()

	assert.Equal(t, viewAgents, m.view)
	assert.Empty(t, m.crumbs)
	assert.Empty(t, m.selectedAgentID)
	assert.Empty(t, m.selectedProjectID)
}
