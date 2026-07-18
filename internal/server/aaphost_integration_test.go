//go:build integration

package server

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/geoffjay/horde/internal/aap"
)

// TestSpawnAAPAgent_MockBinary spawns the real horde aap-mock subprocess via
// the server, asserting the kind branch + handshake works against the
// shipped conformance fixture. Skipped when bin/horde is absent.
func TestSpawnAAPAgent_MockBinary(t *testing.T) {
	bin := findHordeBinaryLocal(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	srv, err := New(Config{
		Mode:              ModeMaster,
		AgentCommand:      bin,
		SpawnDefaultAgent: false,
		AgentDefs: map[string]AgentDef{
			"mock": {Kind: AgentKindAAP, Command: bin, Args: []string{"aap-mock"}},
		},
		ReadyTimeout: 5 * time.Second,
	})
	require.NoError(t, err)

	id, err := srv.SpawnAgent(ctx, "mock")
	require.NoError(t, err)
	defer func() { _ = srv.StopAgent(id) }()

	assert.True(t, srv.IsAAPAgent(id), "spawned mock should be an AAP agent")
	assert.Equal(t, "", srv.AgentSocket(id), "AAP agent has no unix socket")

	// Invoke a turn through the AAPInvoke path and assert a token event
	// arrives with the mock's reply.
	evCh, errCh := srv.AAPInvoke(ctx, id, "", "", "hi")
	var events []AAPStreamEvent
	for ev := range evCh {
		events = append(events, ev)
	}
	require.NoError(t, <-errCh)
	require.NotEmpty(t, events, "expected at least one stream event")
	// The mock's reply is "mock: hi"; find a token event carrying it.
	var sawReply bool
	for _, ev := range events {
		if strings.Contains(string(ev.Data), "mock: hi") {
			sawReply = true
		}
	}
	assert.True(t, sawReply, "expected the mock reply in the stream; got %v", events)
}

// TestSpawnAAPAgent_PiAdapter drives the real pi-aap adapter (an external AAP
// adapter for the pi coding agent) through the host: spawn, the
// initialize→ready handshake, and shutdown. It is the real-adapter counterpart
// to TestSpawnAAPAgent_MockBinary, which uses the in-repo mock.
//
// The adapter lives in a separate repository and needs Node.js, so the test is
// opt-in: set HORDE_TEST_PI_ADAPTER to the built entry point
// (…/pi-aap/packages/pi-adapter/dist/index.js). It skips otherwise, so CI stays
// green without the adapter present. The test asserts only the handshake, which
// the adapter completes without contacting a model; a live turn additionally
// needs a pi provider key and network and is verified manually (see the AAP
// concept doc).
func TestSpawnAAPAgent_PiAdapter(t *testing.T) {
	entry := os.Getenv("HORDE_TEST_PI_ADAPTER")
	if entry == "" {
		t.Skip("set HORDE_TEST_PI_ADAPTER to the pi-aap dist/index.js to run this test")
	}
	if _, err := os.Stat(entry); err != nil {
		t.Skipf("HORDE_TEST_PI_ADAPTER points at %q which is not present: %v", entry, err)
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not found on PATH; required to run the pi-aap adapter")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	srv, err := New(Config{
		Mode:              ModeMaster,
		SpawnDefaultAgent: false,
		AgentDefs: map[string]AgentDef{
			"pi": {Kind: AgentKindAAP, Command: node, Args: []string{entry}},
		},
		ReadyTimeout: 30 * time.Second,
	})
	require.NoError(t, err)

	id, err := srv.SpawnAgent(ctx, "pi")
	require.NoError(t, err, "the pi adapter should complete the initialize→ready handshake")
	defer func() { _ = srv.StopAgent(id) }()

	assert.True(t, srv.IsAAPAgent(id), "the pi adapter is an AAP agent")
	assert.Equal(t, "", srv.AgentSocket(id), "an AAP agent has no unix socket")
}

// TestIsAAPAgent_ADKIsFalse asserts a native ADK agent is not an AAP agent.
func TestIsAAPAgent_ADKIsFalse(t *testing.T) {
	bin := findHordeBinaryLocal(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv, err := New(Config{Mode: ModeMaster, AgentCommand: bin, SpawnDefaultAgent: false})
	require.NoError(t, err)
	id, err := srv.SpawnAgent(ctx, "greeter")
	require.NoError(t, err)
	defer func() { _ = srv.StopAgent(id) }()
	assert.False(t, srv.IsAAPAgent(id), "greeter is a native ADK agent")
}

// findHordeBinaryLocal returns the path to the built horde binary at
// bin/horde, mirroring the external server_test.findHordeBinary. Tests that
// spawn the `horde aap-mock` subprocess skip when the binary is absent.
func findHordeBinaryLocal(t *testing.T) string {
	t.Helper()
	candidates := []string{
		filepath.Join("..", "..", "bin", "horde"),
	}
	for _, c := range candidates {
		abs, err := filepath.Abs(c)
		if err != nil {
			continue
		}
		if info, err := os.Stat(abs); err == nil && !info.IsDir() {
			return abs
		}
	}
	t.Skip("horde binary not found — run `go build -o bin/horde .` before running subprocess tests")
	return ""
}

// TestAAPInvoke_SecondInvokeReplaysBuffer asserts a second AAPInvoke with the
// same invocation id replays the buffered events (Last-Event-ID resume).
func TestAAPInvoke_SecondInvokeReplaysBuffer(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s, cleanup := newTestAAPSession(t, ctx, "fake", AgentDef{Kind: AgentKindAAP, Command: "test"}, false)
	defer cleanup()

	srv, err := New(Config{Mode: ModeMaster, SpawnDefaultAgent: false})
	require.NoError(t, err)
	// Inject the in-process session as a proc so AAPInvoke finds it.
	srv.mu.Lock()
	srv.procs["fake"] = &agentProc{
		id:         "fake",
		name:       "fake",
		kind:       AgentKindAAP,
		state:      AgentRunning,
		doneCh:     make(chan struct{}),
		aapSession: s,
	}
	srv.mu.Unlock()

	// First invoke runs the turn.
	evCh, errCh := srv.AAPInvoke(ctx, "fake", "", "inv-1", "hello")
	var first []AAPStreamEvent
	for ev := range evCh {
		first = append(first, ev)
	}
	require.NoError(t, <-errCh)
	require.NotEmpty(t, first)

	// Second invoke with the same invocation id replays the buffer without
	// starting a new turn.
	evCh2, errCh2 := srv.AAPInvoke(ctx, "fake", "", "inv-1", "hello")
	var replayed []AAPStreamEvent
	for ev := range evCh2 {
		replayed = append(replayed, ev)
	}
	require.NoError(t, <-errCh2)
	assert.NotEmpty(t, replayed, "replay should deliver buffered events")
}

// injectAAPProc registers an in-process AAP session as an agentProc so
// AAPInvoke/RespondApproval can find it, mirroring the setup other AAP invoke
// tests use.
func injectAAPProc(t *testing.T, srv *Server, id string, s *aapHostSession) {
	t.Helper()
	srv.mu.Lock()
	srv.procs[id] = &agentProc{
		id:         id,
		name:       id,
		kind:       AgentKindAAP,
		state:      AgentRunning,
		doneCh:     make(chan struct{}),
		aapSession: s,
	}
	srv.mu.Unlock()
}

// TestAAPInvoke_StreamsBeforeTurnCompletes asserts turn frames stream to the
// client live — a message token arrives while the turn is still blocked on a
// pending approval, before turn_complete. Before the streaming rework the
// client saw nothing until the turn ended (so with auto_approve=false, nothing
// until a decision).
func TestAAPInvoke_StreamsBeforeTurnCompletes(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s, cleanup := newTestAAPSession(t, ctx, "fake", AgentDef{Kind: AgentKindAAP, Command: "test", AutoApprove: false}, true)
	defer cleanup()

	srv, err := New(Config{Mode: ModeMaster, SpawnDefaultAgent: false})
	require.NoError(t, err)
	injectAAPProc(t, srv, "fake", s)

	evCh, errCh := srv.AAPInvoke(ctx, "fake", "", "inv-live", "run tool")

	// The message token must arrive before we resolve the approval; otherwise
	// this loop times out (the old buffer-at-end behavior).
	var sawToken bool
	deadline := time.After(3 * time.Second)
loop:
	for {
		select {
		case ev, ok := <-evCh:
			if !ok {
				break loop
			}
			if ev.Typ == "token" && strings.Contains(string(ev.Data), "fake:") {
				sawToken = true
				break loop
			}
		case <-deadline:
			t.Fatal("no live token arrived before the turn completed")
		}
	}
	require.True(t, sawToken, "expected a live token event while the turn was still pending")

	// Resolve the approval so the fake completes the turn, then drain.
	require.NoError(t, s.resolvePending("req-inv-live", aap.DecisionAllow))
	for range evCh { //nolint:revive // draining the stream to let goroutines finish
	}
	<-errCh
}

// TestAAPInvoke_TurnSurvivesClientDisconnect asserts a client disconnecting
// (e.g. leaving the TUI invoke view) does not cancel the turn: the approval
// stays pending, and resolving it later still drives the turn to completion,
// observable via a reconnecting client.
func TestAAPInvoke_TurnSurvivesClientDisconnect(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s, cleanup := newTestAAPSession(t, ctx, "fake", AgentDef{Kind: AgentKindAAP, Command: "test", AutoApprove: false}, true)
	defer cleanup()

	srv, err := New(Config{Mode: ModeMaster, SpawnDefaultAgent: false})
	require.NoError(t, err)
	injectAAPProc(t, srv, "fake", s)

	clientCtx, clientCancel := context.WithCancel(ctx)
	evCh, errCh := srv.AAPInvoke(clientCtx, "fake", "", "inv-dc", "run tool")

	// Wait until the turn is mid-flight (approval pending).
	require.Eventually(t, func() bool {
		c := s.ctxStore.get("fake")
		return c != nil && len(c.PendingApprovals) == 1
	}, 3*time.Second, 20*time.Millisecond, "approval should become pending")

	// Simulate the client disconnecting / leaving the invoke view.
	clientCancel()
	for range evCh { //nolint:revive // drain the cancelled reader
	}
	<-errCh

	// The turn must NOT have been cancelled by the disconnect.
	c := s.ctxStore.get("fake")
	require.NotNil(t, c)
	require.Len(t, c.PendingApprovals, 1, "disconnect must not cancel the turn")

	// Resolve the approval; the turn (on its own context) completes. A
	// reconnecting client sees it through to done.
	require.NoError(t, s.resolvePending("req-inv-dc", aap.DecisionAllow))

	ev2, err2 := srv.AAPInvoke(ctx, "fake", "", "inv-dc", "run tool")
	var sawDone bool
	for ev := range ev2 {
		if ev.Typ == "done" {
			sawDone = true
		}
	}
	require.NoError(t, <-err2)
	assert.True(t, sawDone, "reconnect should observe the turn completing after approval")
}
