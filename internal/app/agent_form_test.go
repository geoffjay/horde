package app

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/geoffjay/horde/internal/client"
)

func TestAgentForm_CycleSelectors(t *testing.T) {
	m := New(context.Background(), "127.0.0.1:1")
	m.node = client.NodeInfo{NodeID: "n1"}
	m.availableAgents = []client.AvailableAgent{{Name: "greeter", Kind: "adk"}, {Name: "pi", Kind: "aap"}}
	m.openAgentForm()

	// The agent selector is focused first; right cycles the agent index.
	m.cycleAgentField(1)
	assert.Equal(t, 1, m.agentForm.agentIdx)

	// Focus the node selector; right cycles the node index.
	m.agentForm.cursor = agentFormFieldNode
	m.cycleAgentField(1)
	assert.Equal(t, 1, m.agentForm.nodeIdx)
}

func TestAgentForm_SubmitWithNoAvailableAgentsIsNoOp(t *testing.T) {
	m := New(context.Background(), "127.0.0.1:1")
	m.openAgentForm()

	_, cmd := m.submitAgentForm()
	assert.Nil(t, cmd, "nothing valid to spawn")
	assert.True(t, m.agentForm.open, "form stays open when there is nothing to submit")
}

func TestHandleAgentAction_SurfacesSpawnError(t *testing.T) {
	m := New(context.Background(), "127.0.0.1:1")
	_, _ = m.handleAgentAction(&agentActionMsg{err: assertErr("boom")})
	assert.Contains(t, m.actionErr, "boom")

	_, _ = m.handleAgentAction(&agentActionMsg{agent: client.Agent{ID: "a1"}})
	assert.Empty(t, m.actionErr, "a successful action clears the error")
}

// assertErr is a tiny error helper for tests.
type assertErr string

func (e assertErr) Error() string { return string(e) }
