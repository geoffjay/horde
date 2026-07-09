package agents

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNew_ConstructsGreeter(t *testing.T) {
	a, err := New()
	require.NoError(t, err)
	require.NotNil(t, a)

	assert.Equal(t, "greeter", a.Name())
	assert.Equal(t, "A hello-world agent that greets the user.", a.Description())
}

func TestNew_HasNoSubAgents(t *testing.T) {
	a, err := New()
	require.NoError(t, err)
	assert.Empty(t, a.SubAgents())
}

func TestNew_FindAgent_Self(t *testing.T) {
	a, err := New()
	require.NoError(t, err)
	assert.Equal(t, a, a.FindAgent("greeter"))
	assert.Nil(t, a.FindAgent("missing"))
}
