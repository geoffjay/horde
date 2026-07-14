package aap_test

import (
	"bufio"
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/geoffjay/horde/internal/aap"
)

// TestHostSession_DrivesMockAdapter runs the host-side driver against the
// in-tree mock adapter over OS pipes, exercising the full handshake + turn +
// shutdown loop. OS pipes are used (not io.Pipe) so the mock's buffered writes
// after turn_complete do not block a synchronous reader.
func TestHostSession_DrivesMockAdapter(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// host → adapter (adapter reads h2aR; host writes h2aW)
	h2aR, h2aW, err := os.Pipe()
	require.NoError(t, err)
	// adapter → host (host reads a2hR; adapter writes a2hW)
	a2hR, a2hW, err := os.Pipe()
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = h2aR.Close()
		_ = h2aW.Close()
		_ = a2hR.Close()
		_ = a2hW.Close()
	})

	done := make(chan error, 1)
	go func() { done <- aap.RunMockAdapter(ctx, h2aR, a2hW) }()

	sess := aap.NewHostSession(a2hR, h2aW)

	ready, err := sess.Initialize(&aap.Initialize{Workspace: aap.Workspace{Cwd: "/tmp"}})
	require.NoError(t, err)
	require.NotNil(t, ready)
	assert.Equal(t, aap.ProtocolVersion, ready.ProtocolVersion)
	assert.Equal(t, "mock", ready.Agent.Name)
	assert.Contains(t, ready.Capabilities, aap.CapStreaming)

	var text string
	var sawBusy bool
	tc, err := sess.Prompt("t1", aap.TextPrompt("hello"), aap.TurnObserver{
		OnMessage: func(m aap.Message) {
			for _, b := range m.Content {
				if b.Type == aap.BlockText {
					text += b.Text
				}
			}
		},
		OnStatus: func(s aap.Status) {
			if s.State == aap.StateBusy {
				sawBusy = true
			}
		},
	})
	require.NoError(t, err)
	require.NotNil(t, tc)
	assert.Equal(t, "t1", tc.TurnID)
	assert.False(t, tc.IsError)
	require.NotNil(t, tc.StopReason)
	assert.Equal(t, "end_turn", *tc.StopReason)
	assert.Equal(t, "mock: hello", text)
	assert.True(t, sawBusy, "expected a busy status during the turn")

	require.NoError(t, sess.Shutdown())

	select {
	case err := <-done:
		assert.NoError(t, err)
	case <-ctx.Done():
		t.Fatal("mock adapter did not exit after shutdown")
	}
}

// TestHostSession_ApprovalDefaultDeny verifies the fail-safe: with a nil
// Approve callback the session answers an approval request with deny. Frames
// are hand-fed so the exact request/response exchange is observable.
func TestHostSession_ApprovalDefaultDeny(t *testing.T) {
	a2hR, a2hW, err := os.Pipe() // agent → host (test writes, session reads)
	require.NoError(t, err)
	h2aR, h2aW, err := os.Pipe() // host → adapter (session writes, test reads)
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = a2hR.Close()
		_ = a2hW.Close()
		_ = h2aR.Close()
		_ = h2aW.Close()
	})

	sess := aap.NewHostSession(a2hR, h2aW)
	hostReader := bufio.NewReader(h2aR)

	type result struct {
		tc  *aap.TurnComplete
		err error
	}
	ch := make(chan result, 1)
	go func() {
		tc, err := sess.Prompt("t1", aap.TextPrompt("x"), aap.TurnObserver{})
		ch <- result{tc, err}
	}()

	// The session first writes the prompt frame.
	line, err := aap.ReadLine(hostReader)
	require.NoError(t, err)
	hm, err := aap.ParseHostMessage(line)
	require.NoError(t, err)
	_, ok := hm.(aap.Prompt)
	require.True(t, ok, "expected prompt frame, got %T", hm)

	// Feed an approval request; the session must answer deny (nil Approve).
	require.NoError(t, aap.WriteMessage(a2hW, aap.ApprovalRequest{RequestID: "r1", ToolName: "Bash"}))

	line, err = aap.ReadLine(hostReader)
	require.NoError(t, err)
	hm, err = aap.ParseHostMessage(line)
	require.NoError(t, err)
	resp, ok := hm.(aap.ApprovalResponse)
	require.True(t, ok, "expected approval_response, got %T", hm)
	assert.Equal(t, "r1", resp.RequestID)
	assert.Equal(t, aap.DecisionDeny, resp.Decision)

	// Close the turn.
	require.NoError(t, aap.WriteMessage(a2hW, aap.TurnComplete{TurnID: "t1"}))
	r := <-ch
	require.NoError(t, r.err)
	require.NotNil(t, r.tc)
}
