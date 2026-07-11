package aap

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// parseAgentFrames splits NDJSON output into typed agent messages.
func parseAgentFrames(t *testing.T, raw []byte) []AgentMessage {
	t.Helper()
	var msgs []AgentMessage
	for _, line := range bytes.Split(raw, []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		m, err := ParseAgentMessage(line)
		require.NoError(t, err)
		msgs = append(msgs, m)
	}
	return msgs
}

func TestRunMockAdapter(t *testing.T) {
	var in bytes.Buffer
	require.NoError(t, WriteMessage(&in, Initialize{
		ProtocolVersion: ProtocolVersion,
		Workspace:       Workspace{Cwd: "/repo"},
	}))
	require.NoError(t, WriteMessage(&in, Prompt{TurnID: "t1", Content: TextPrompt("hi")}))
	require.NoError(t, WriteMessage(&in, Shutdown{}))

	var out bytes.Buffer
	require.NoError(t, RunMockAdapter(context.Background(), &in, &out))

	msgs := parseAgentFrames(t, out.Bytes())
	require.Len(t, msgs, 5)

	ready, ok := msgs[0].(Ready)
	require.True(t, ok)
	assert.Equal(t, ProtocolVersion, ready.ProtocolVersion)
	assert.Contains(t, ready.Capabilities, CapStreaming)

	assert.Equal(t, Status{State: StateBusy}, msgs[1])

	msg, ok := msgs[2].(Message)
	require.True(t, ok)
	assert.Equal(t, "t1", msg.TurnID)
	require.Len(t, msg.Content, 1)
	assert.Equal(t, "mock: hi", msg.Content[0].Text)

	tc, ok := msgs[3].(TurnComplete)
	require.True(t, ok)
	assert.Equal(t, "t1", tc.TurnID)
	assert.False(t, tc.IsError)
	require.NotNil(t, tc.Usage)
	assert.Equal(t, uint64(1), tc.Usage.NumTurns)

	assert.Equal(t, Status{State: StateIdle}, msgs[4])
}

func TestRunMockAdapter_VersionMismatch(t *testing.T) {
	var in bytes.Buffer
	require.NoError(t, WriteMessage(&in, Initialize{
		ProtocolVersion: ProtocolVersion + 1,
		Workspace:       Workspace{Cwd: "/repo"},
	}))

	var out bytes.Buffer
	require.NoError(t, RunMockAdapter(context.Background(), &in, &out))

	msgs := parseAgentFrames(t, out.Bytes())
	require.Len(t, msgs, 1)
	e, ok := msgs[0].(Error)
	require.True(t, ok)
	assert.True(t, e.Fatal)
	require.NotNil(t, e.Code)
	assert.Equal(t, "unsupported_version", *e.Code)
}

// A frame before initialize is rejected with a fatal error and the adapter
// exits.
func TestRunMockAdapter_PromptBeforeInitialize(t *testing.T) {
	var in bytes.Buffer
	require.NoError(t, WriteMessage(&in, Prompt{TurnID: "t1", Content: TextPrompt("hi")}))

	var out bytes.Buffer
	require.NoError(t, RunMockAdapter(context.Background(), &in, &out))

	msgs := parseAgentFrames(t, out.Bytes())
	require.Len(t, msgs, 1)
	e, ok := msgs[0].(Error)
	require.True(t, ok)
	assert.True(t, e.Fatal)
}
