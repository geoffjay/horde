package server

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAvailableAgents(t *testing.T) {
	srv, err := New(Config{
		SpawnDefaultAgent: false,
		AgentDefs:         map[string]AgentDef{"pi": {Kind: AgentKindAAP, Command: "pi-aap"}},
	})
	require.NoError(t, err)

	avail := srv.AvailableAgents()

	kinds := make(map[string]AgentKind, len(avail))
	for _, a := range avail {
		kinds[a.Name] = a.Kind
	}
	// Built-in ADK registry agents are listed.
	assert.Equal(t, AgentKindADK, kinds["greeter"])
	// Configured AAP definitions are listed with their kind.
	assert.Equal(t, AgentKindAAP, kinds["pi"])

	// Sorted by name.
	for i := 1; i < len(avail); i++ {
		assert.LessOrEqual(t, avail[i-1].Name, avail[i].Name)
	}
}
