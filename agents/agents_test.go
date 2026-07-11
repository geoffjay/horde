package agents

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGet_Greeter(t *testing.T) {
	a, err := Get("greeter")
	require.NoError(t, err)
	require.NotNil(t, a)

	assert.Equal(t, "greeter", a.Name())
	assert.Equal(t, "A hello-world agent that greets the user.", a.Description())
}

func TestGet_Repeater(t *testing.T) {
	a, err := Get("repeater")
	require.NoError(t, err)
	require.NotNil(t, a)

	assert.Equal(t, "repeater", a.Name())
	assert.Equal(t, "An agent that repeats the user's message and counts turns.", a.Description())
}

func TestGet_UnknownAgent(t *testing.T) {
	_, err := Get("nonexistent")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown agent")
}

func TestNames(t *testing.T) {
	names := Names()
	assert.Contains(t, names, "greeter")
	assert.Contains(t, names, "repeater")
}

func TestGet_HasNoSubAgents(t *testing.T) {
	a, err := Get("greeter")
	require.NoError(t, err)
	assert.Empty(t, a.SubAgents())
}

func TestGet_FindAgent_Self(t *testing.T) {
	a, err := Get("greeter")
	require.NoError(t, err)
	assert.Equal(t, a, a.FindAgent("greeter"))
	assert.Nil(t, a.FindAgent("missing"))
}
