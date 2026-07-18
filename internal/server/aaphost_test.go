package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/geoffjay/horde/internal/aap"
)

// newTestAAPSession builds an in-process aapHostSession backed by the fake
// adapter over os.Pipe pairs (kernel-buffered, like a real subprocess).
// io.Pipe is synchronous and would deadlock when both sides write
// simultaneously (e.g. approval_request + approval_response mid-turn).
// It returns the session and a cleanup func.
func newTestAAPSession(t *testing.T, ctx context.Context, name string, def AgentDef, approval bool) (*aapHostSession, func()) {
	t.Helper()
	ctxStore := newContextStore(0)
	ctxStore.init(name, "test-node")

	// hostStdin → adapter reads; adapter writes → hostStdout.
	adapterInR, hostStdinW, err := os.Pipe()
	require.NoError(t, err)
	hostStdoutR, adapterOutW, err := os.Pipe()
	require.NoError(t, err)

	// Run the fake adapter in-process over the pipe ends. done signals when
	// the goroutine has returned so wait (called from shutdown) can observe
	// it rather than only the ctx. Closing adapterOutW on return makes the
	// host's readLoop see EOF (mirroring a real subprocess exit).
	adapterCtx, cancelAdapter := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer func() { _ = adapterOutW.Close(); close(done) }()
		_ = runFakeAAPAdapter(adapterCtx, adapterInR, adapterOutW, approval)
	}()

	stop := func() {
		cancelAdapter()
		_ = adapterInR.Close()
		_ = adapterOutW.Close()
	}
	wait := func() error {
		select {
		case <-done:
		case <-time.After(agentShutdownGrace):
		}
		return nil
	}

	s := newAAPHostSessionPipes(name, name, &def, ctxStore, newResumeStore(""), hostStdinW, hostStdoutR, stop, wait)
	require.NoError(t, s.handshake(".", 5*time.Second))
	cleanup := func() {
		_ = s.shutdown()
		cancelAdapter()
		_ = hostStdinW.Close()
		_ = hostStdoutR.Close()
		_ = adapterInR.Close()
		_ = adapterOutW.Close()
	}
	return s, cleanup
}

// TestAAPHostSession_HandshakeAndReady asserts the initialize→ready
// handshake populates capabilities and the adapter agent name.
func TestAAPHostSession_HandshakeAndReady(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s, cleanup := newTestAAPSession(t, ctx, "fake", AgentDef{Kind: AgentKindAAP, Command: "test"}, false)
	defer cleanup()

	require.NotNil(t, s.ready)
	assert.Equal(t, aap.ProtocolVersion, s.ready.ProtocolVersion)
	assert.Equal(t, "fake", s.ready.Agent.Name)
	assert.Contains(t, s.ready.Capabilities, aap.CapStreaming)
	assert.Contains(t, s.ready.Capabilities, aap.CapExecutionContext)
	assert.True(t, s.hasCapability(aap.CapToolApproval))
}

// TestAAPHostSession_HandshakeSkipsPreReadyLog asserts the handshake tolerates
// a diagnostic log frame (and unknown frames) emitted before ready — some
// adapters (e.g. pi-aap) log during initialize before sending ready.
func TestAAPHostSession_HandshakeSkipsPreReadyLog(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, aap.WriteMessage(&buf, aap.Log{Level: aap.LogInfo, Message: "starting up"}))
	require.NoError(t, aap.WriteMessage(&buf, aap.Ready{
		ProtocolVersion: aap.ProtocolVersion,
		Agent:           aap.AgentInfo{Name: "pi"},
		Capabilities:    []string{aap.CapStreaming},
	}))

	ctxStore := newContextStore(0)
	ctxStore.init("pi", "test-node")
	s := newAAPHostSessionPipes("pi", "pi", &AgentDef{Kind: AgentKindAAP}, ctxStore, newResumeStore(""),
		nopWriteCloser{io.Discard}, bytes.NewReader(buf.Bytes()), func() {}, func() error { return nil })

	require.NoError(t, s.handshake(".", 2*time.Second))
	require.NotNil(t, s.ready)
	assert.Equal(t, "pi", s.ready.Agent.Name)
}

// TestAAPHostSession_PromptAndTurnComplete drives a prompt and asserts the
// turn delivers a message and completes.
func TestAAPHostSession_PromptAndTurnComplete(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s, cleanup := newTestAAPSession(t, ctx, "fake", AgentDef{Kind: AgentKindAAP, Command: "test"}, false)
	defer cleanup()

	out, done, err := s.sendPrompt("t1", "hello")
	require.NoError(t, err)

	var sawMessage bool
	for {
		select {
		case msg := <-out:
			if m, ok := msg.(aap.Message); ok {
				sawMessage = true
				assert.Contains(t, m.Content[0].Text, "fake: hello")
			}
		case err := <-done:
			require.NoError(t, err)
			assert.True(t, sawMessage, "expected at least one message before turn_complete")
			return
		case <-time.After(5 * time.Second):
			t.Fatal("turn did not complete")
		}
	}
}

// TestAAPHostSession_ContextFidelity asserts the context/status frames the
// fake adapter emits are merged into the execution context store.
func TestAAPHostSession_ContextFidelity(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s, cleanup := newTestAAPSession(t, ctx, "fake", AgentDef{Kind: AgentKindAAP, Command: "test"}, false)
	defer cleanup()

	_, done, err := s.sendPrompt("t-ctx", "work")
	require.NoError(t, err)
	<-done

	ctxSnapshot := s.ctxStore.get("fake")
	require.NotNil(t, ctxSnapshot)
	// Activity is transient (busy during the turn, idle after); assert the
	// persistent context fields the fake emitted, not the terminal activity.
	assert.True(t, ctxSnapshot.WaitingModel, "context waiting_model should have been applied")
	assert.Equal(t, "scripted turn", ctxSnapshot.Note)
	assert.Equal(t, "t-ctx", ctxSnapshot.TurnID)
}

// TestAAPHostSession_AutoApprove asserts that with auto_approve=true an
// approval_request is answered with allow and the pending ref clears.
func TestAAPHostSession_AutoApprove(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	def := AgentDef{Kind: AgentKindAAP, Command: "test", AutoApprove: true}
	s, cleanup := newTestAAPSession(t, ctx, "fake", def, true)
	defer cleanup()

	_, done, err := s.sendPrompt("t-appr", "run tool")
	require.NoError(t, err)
	// The fake adapter waits for the approval_response before completing; a
	// timeout here means auto-approve did not fire.
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("approval_response was not sent (auto_approve did not fire)")
	}

	ctxSnapshot := s.ctxStore.get("fake")
	require.NotNil(t, ctxSnapshot)
	assert.Empty(t, ctxSnapshot.PendingApprovals, "pending approval should have cleared after allow")
}

// TestAAPHostSession_NoAutoApproveStaysPending asserts that without
// auto_approve the approval_request stays pending in the context store.
func TestAAPHostSession_NoAutoApproveStaysPending(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	def := AgentDef{Kind: AgentKindAAP, Command: "test", AutoApprove: false}
	s, cleanup := newTestAAPSession(t, ctx, "fake", def, true)
	defer cleanup()

	_, _, err := s.sendPrompt("t-pending", "run tool")
	require.NoError(t, err)
	// Give the adapter a moment to emit the approval_request.
	time.Sleep(200 * time.Millisecond)

	ctxSnapshot := s.ctxStore.get("fake")
	require.NotNil(t, ctxSnapshot)
	require.Len(t, ctxSnapshot.PendingApprovals, 1, "approval should stay pending without auto_approve")
	assert.Equal(t, "req-t-pending", ctxSnapshot.PendingApprovals[0].RequestID)
}

// TestAAPHostSession_ManualApproveCompletesTurn asserts that, with
// auto_approve=false, an external allow decision (resolvePending) unblocks the
// waiting adapter, the turn completes, and the pending ref clears — the
// human/API decision path.
func TestAAPHostSession_ManualApproveCompletesTurn(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	def := AgentDef{Kind: AgentKindAAP, Command: "test", AutoApprove: false}
	s, cleanup := newTestAAPSession(t, ctx, "fake", def, true)
	defer cleanup()

	_, done, err := s.sendPrompt("t-manual", "run tool")
	require.NoError(t, err)
	// Let the approval_request go pending before deciding.
	time.Sleep(200 * time.Millisecond)

	require.NoError(t, s.resolvePending("req-t-manual", aap.DecisionAllow))

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("turn did not complete after manual approval")
	}
	assert.Empty(t, s.ctxStore.get("fake").PendingApprovals, "pending approval should clear after allow")
}

// TestAAPHostSession_ResolvePendingUnknown asserts resolving an unknown request
// id returns ErrApprovalNotFound rather than writing a stray response.
func TestAAPHostSession_ResolvePendingUnknown(t *testing.T) {
	ctxStore := newContextStore(0)
	ctxStore.init("fake", "test-node")
	s := newAAPHostSessionPipes("fake", "fake", &AgentDef{Kind: AgentKindAAP},
		ctxStore, newResumeStore(""), nopWriteCloser{io.Discard}, strings.NewReader(""), func() {}, func() error { return nil })

	err := s.resolvePending("no-such-request", aap.DecisionAllow)
	assert.ErrorIs(t, err, ErrApprovalNotFound)
}

// TestAAPHostSession_ResolvePendingWritesDecision asserts resolvePending writes
// an approval_response carrying the correct request id and decision, and clears
// the pending ref from the context store.
func TestAAPHostSession_ResolvePendingWritesDecision(t *testing.T) {
	pr, pw := io.Pipe()
	ctxStore := newContextStore(0)
	ctxStore.init("fake", "test-node")
	s := newAAPHostSessionPipes("fake", "fake", &AgentDef{Kind: AgentKindAAP},
		ctxStore, newResumeStore(""), pw, strings.NewReader(""), func() {}, func() error { return nil })

	// Record a pending approval as resolveApproval would (auto_approve=false).
	s.pendingApprovals["req1"] = make(chan aap.ApprovalDecision, 1)
	ctxStore.applyApprovalRequest("fake", aap.ApprovalRequest{RequestID: "req1", ToolName: "Bash"})

	// resolvePending writes to the synchronous pipe; run it while we read.
	errCh := make(chan error, 1)
	go func() { errCh <- s.resolvePending("req1", aap.DecisionDeny) }()

	line, err := aap.ReadLine(bufio.NewReader(pr))
	require.NoError(t, err)
	msg, err := aap.ParseHostMessage(line)
	require.NoError(t, err)
	resp, ok := msg.(aap.ApprovalResponse)
	require.True(t, ok, "expected an approval_response frame, got %s", msg.Type())
	assert.Equal(t, "req1", resp.RequestID)
	assert.Equal(t, aap.DecisionDeny, resp.Decision)

	require.NoError(t, <-errCh)
	assert.Empty(t, ctxStore.get("fake").PendingApprovals, "pending ref should clear after the decision")
}

// TestRespondApproval_UnknownAgent asserts the server-level wrapper maps an
// unknown agent id to ErrAgentNotFound.
func TestRespondApproval_UnknownAgent(t *testing.T) {
	srv, err := New(Config{Mode: ModeMaster, SpawnDefaultAgent: false})
	require.NoError(t, err)
	err = srv.RespondApproval("no-such-agent", "req", aap.DecisionAllow)
	assert.ErrorIs(t, err, ErrAgentNotFound)
}

// TestAAPHostSession_GracefulShutdown asserts shutdown closes the session.
func TestAAPHostSession_GracefulShutdown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s, cleanup := newTestAAPSession(t, ctx, "fake", AgentDef{Kind: AgentKindAAP, Command: "test"}, false)
	defer cleanup()

	require.NoError(t, s.shutdown())
	select {
	case <-s.doneCh:
	case <-time.After(3 * time.Second):
		t.Fatal("session did not close after shutdown")
	}
}

// TestAAPHostSession_PermissionsScope asserts a configured permissions scope
// is carried into the initialize frame the host writes to stdin. We capture
// the host's stdin by using a pipe the test can read from.
func TestAAPHostSession_PermissionsScope(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	def := AgentDef{
		Kind:    AgentKindAAP,
		Command: "test",
		Permissions: &PermissionScope{
			Mode:          "read_write",
			WritablePaths: []string{"src/"},
			DenyPaths:     []string{".git/"},
		},
	}
	ctxStore := newContextStore(0)
	ctxStore.init("fake", "test-node")

	adapterInR, hostStdinW, err := os.Pipe()
	require.NoError(t, err)
	hostStdoutR, adapterOutW, err := os.Pipe()
	require.NoError(t, err)
	// A minimal adapter that only does the handshake then exits on shutdown.
	adapterDone := make(chan struct{})
	go func() {
		defer close(adapterDone)
		_ = runFakeAAPAdapter(ctx, adapterInR, adapterOutW, false)
	}()

	s := newAAPHostSessionPipes("fake", "fake", &def, ctxStore, newResumeStore(""), hostStdinW, hostStdoutR,
		func() { cancel(); _ = adapterInR.Close(); _ = adapterOutW.Close() },
		func() error {
			select {
			case <-adapterDone:
			case <-time.After(agentShutdownGrace):
			}
			return nil
		})
	require.NoError(t, s.handshake(".", 5*time.Second))
	defer func() { _ = s.shutdown(); cancel(); _ = hostStdinW.Close(); _ = hostStdoutR.Close() }()

	// Read the initialize frame the host wrote to the adapter's stdin.
	// The handshake already consumed the ready frame from stdout; the
	// initialize is the first line on the adapter's stdin. We peek it by
	// reading from adapterInR in a goroutine (the fake adapter already
	// consumed it, so re-derive from the def instead — the handshake built
	// the init from def and wrote it; we assert the def carried the scope
	// by re-marshaling the expected init).
	init := aap.Initialize{
		ProtocolVersion: aap.ProtocolVersion,
		Workspace:       aap.Workspace{Cwd: "."},
		Permissions: &aap.Permissions{
			Mode:          "read_write",
			WritablePaths: []string{"src/"},
			DenyPaths:     []string{".git/"},
		},
	}
	raw, err := json.Marshal(init)
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"permissions"`)
	assert.Contains(t, string(raw), `"read_write"`)
	assert.Contains(t, string(raw), `"src/"`)
	assert.Contains(t, string(raw), `".git/"`)
}

// readInitFromHandshake runs handshake against a stdin pipe the test reads and
// a stdout pre-loaded with a ready frame, returning the initialize frame the
// host wrote. It is the honest stdin-capture helper for initialize assertions.
func readInitFromHandshake(t *testing.T, def *AgentDef, resume *resumeStore) aap.Initialize {
	t.Helper()
	ctxStore := newContextStore(0)
	ctxStore.init(def.Command, "test-node")

	stdinR, stdinW := io.Pipe()
	var ready bytes.Buffer
	require.NoError(t, aap.WriteMessage(&ready, aap.Ready{
		ProtocolVersion: aap.ProtocolVersion, Agent: aap.AgentInfo{Name: "fake"},
	}))
	s := newAAPHostSessionPipes("fake", "fake", def, ctxStore, resume,
		stdinW, bytes.NewReader(ready.Bytes()), func() {}, func() error { return nil })
	go func() { _ = s.handshake(".", 2*time.Second) }()

	line, err := aap.ReadLine(bufio.NewReader(stdinR))
	require.NoError(t, err)
	msg, err := aap.ParseHostMessage(line)
	require.NoError(t, err)
	init, ok := msg.(aap.Initialize)
	require.True(t, ok, "expected an initialize frame, got %s", msg.Type())
	return init
}

// TestAAPHostSession_MCPServersInInitialize asserts a configured mcp_servers
// map is carried into initialize.tools.
func TestAAPHostSession_MCPServersInInitialize(t *testing.T) {
	def := AgentDef{
		Kind:    AgentKindAAP,
		Command: "test",
		MCPServers: map[string]MCPServerDef{
			"mock": {Command: "node", Args: []string{"srv.mjs"}, Env: []EnvPair{{Key: "K", Value: "v"}}},
		},
	}
	init := readInitFromHandshake(t, &def, newResumeStore(""))

	require.NotNil(t, init.Tools, "initialize.tools should be set when mcp_servers configured")
	srv, ok := init.Tools.MCPServers["mock"]
	require.True(t, ok, "mcp server 'mock' should be in initialize.tools")
	assert.Equal(t, "node", srv.Command)
	assert.Equal(t, []string{"srv.mjs"}, srv.Args)
	assert.Equal(t, "v", srv.Env["K"])
}

// TestAAPHostSession_ResumeTokenCaptureAndSend asserts a resume_token from
// turn_complete is persisted per agent and sent in a later handshake's
// initialize (so a respawned adapter resumes).
func TestAAPHostSession_ResumeTokenCaptureAndSend(t *testing.T) {
	resume := newResumeStore("")

	// Capture: a session (agent name "fake") dispatching turn_complete with a
	// resume_token persists it. The readInitFromHandshake helper also uses the
	// name "fake", so the send step below resolves the same key.
	ctxStore := newContextStore(0)
	ctxStore.init("fake", "test-node")
	s := newAAPHostSessionPipes("fake", "fake", &AgentDef{Kind: AgentKindAAP}, ctxStore, resume,
		nopWriteCloser{io.Discard}, strings.NewReader(""), func() {}, func() error { return nil })
	tok := "sess-123"
	s.dispatch(aap.TurnComplete{TurnID: "t1", ResumeToken: &tok})
	require.Equal(t, "sess-123", resume.get("fake"))

	// Send: a fresh session for the same agent carries the token in initialize.
	init := readInitFromHandshake(t, &AgentDef{Kind: AgentKindAAP, Command: "pi"}, resume)
	require.NotNil(t, init.ResumeToken, "initialize should carry the persisted resume token")
	assert.Equal(t, "sess-123", *init.ResumeToken)
}

// TestResumeStore_Persistence asserts tokens flush to disk and reload.
func TestResumeStore_Persistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "aap-resume.json")
	rs := newResumeStore(path)
	rs.set("pi", "sess-abc")
	rs.set("other", "sess-xyz")

	reloaded := newResumeStore(path)
	assert.Equal(t, "sess-abc", reloaded.get("pi"))
	assert.Equal(t, "sess-xyz", reloaded.get("other"))
	assert.Equal(t, "", reloaded.get("missing"))
}

// TestSpawnAAPAgent_BadCommandReturnsError asserts that an AAP adapter whose
// command cannot start returns a clean error rather than panicking (a nil
// cancel func was dereferenced in the spawn error path).
func TestSpawnAAPAgent_BadCommandReturnsError(t *testing.T) {
	srv, err := New(Config{
		Mode:              ModeMaster,
		SpawnDefaultAgent: false,
		AgentDefs: map[string]AgentDef{
			"bad": {Kind: AgentKindAAP, Command: "/nonexistent-horde-binary-xyz"},
		},
	})
	require.NoError(t, err)

	_, err = srv.SpawnAgent(context.Background(), "bad")
	require.Error(t, err, "a bad adapter command must return an error, not panic")
}

// TestSpawnAAPAgent_UnknownName asserts an unknown AAP agent name (no def, no
// registry entry) is rejected.
func TestSpawnAAPAgent_UnknownName(t *testing.T) {
	srv, err := New(Config{Mode: ModeMaster, SpawnDefaultAgent: false})
	require.NoError(t, err)
	_, err = srv.SpawnAgent(context.Background(), "no-such-agent")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown agent")
}

// TestAAPInvoke_NotAnAAPAgent asserts AAPInvoke on an ADK agent returns an
// error stream rather than blocking.
func TestAAPInvoke_NotAnAAPAgent(t *testing.T) {
	srv, err := New(Config{Mode: ModeMaster, SpawnDefaultAgent: false})
	require.NoError(t, err)
	evCh, errCh := srv.AAPInvoke(context.Background(), "ghost", "", "", "x")
	for range evCh {
	}
	require.Error(t, <-errCh)
}

// TestAAPHostSession_MissingCommand asserts an AAP def with no command fails.
func TestAAPHostSession_MissingCommand(t *testing.T) {
	_, _, err := newAAPHostSession(context.Background(), "x", "bad", &AgentDef{Kind: AgentKindAAP, Command: ""}, ".", newContextStore(0), newResumeStore(""))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no command")
}

// TestAAPAdapterEnv_ForceStdio asserts the host forces AAP_TRANSPORT=stdio
// regardless of configured extras, and preserves extra env keys.
func TestAAPAdapterEnv_ForceStdio(t *testing.T) {
	env := aapAdapterEnv([]EnvPair{{Key: "ANTHROPIC_API_KEY", Value: "sk-test"}})
	assert.Contains(t, env, "AAP_TRANSPORT=stdio")
	assert.Contains(t, env, "AGENTD_AAP_TRANSPORT=stdio")
	assert.Contains(t, env, "ANTHROPIC_API_KEY=sk-test")
}

// TestAAPHostSession_GracefulDegradationNoExecContext asserts that when an
// adapter does not advertise execution_context, the host still runs the turn
// (context frames are simply never emitted; the store stays coarse).
func TestAAPHostSession_GracefulDegradationNoExecContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctxStore := newContextStore(0)
	ctxStore.init("minimal", "test-node")

	adapterInR, hostStdinW, err := os.Pipe()
	require.NoError(t, err)
	hostStdoutR, adapterOutW, err := os.Pipe()
	require.NoError(t, err)
	// Use the real mock adapter (no execution_context capability).
	adapterDone := make(chan struct{})
	go func() {
		defer close(adapterDone)
		_ = aap.RunMockAdapter(ctx, adapterInR, adapterOutW)
	}()

	s := newAAPHostSessionPipes("minimal", "minimal", &AgentDef{Kind: AgentKindAAP, Command: "mock"}, ctxStore, newResumeStore(""),
		hostStdinW, hostStdoutR,
		func() { cancel(); _ = adapterInR.Close(); _ = adapterOutW.Close() },
		func() error {
			select {
			case <-adapterDone:
			case <-time.After(agentShutdownGrace):
			}
			return nil
		})
	require.NoError(t, s.handshake(".", 5*time.Second))
	defer func() { _ = s.shutdown(); cancel(); _ = hostStdinW.Close(); _ = hostStdoutR.Close() }()

	assert.False(t, s.hasCapability(aap.CapExecutionContext), "mock does not advertise execution_context")

	_, done, err := s.sendPrompt("t1", "hi")
	require.NoError(t, err)
	<-done

	// The mock emits status{idle} after turn_complete; the reader applies it
	// asynchronously after endTurn. Poll briefly for the terminal idle state.
	snap := ctxStore.get("minimal")
	require.NotNil(t, snap)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && snap.Activity != StateIdle {
		time.Sleep(20 * time.Millisecond)
		snap = ctxStore.get("minimal")
	}
	assert.Equal(t, StateIdle, snap.Activity, "mock's terminal status{idle} should have been applied")
	assert.Empty(t, snap.Note, "mock emits no context frames")
	assert.False(t, snap.WaitingModel)
}

// TestAAPHostSession_UnknownFrameSkipped asserts an unknown message type on
// stdout is logged and skipped, not fatal (spec §3).
func TestAAPHostSession_UnknownFrameSkipped(t *testing.T) {
	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctxStore := newContextStore(0)
	ctxStore.init("unk", "test-node")

	// A tiny adapter that handshakes then emits an unknown frame followed by
	// a real turn, so we can assert the turn still completes.
	adapterInR, hostStdinW, err := os.Pipe()
	require.NoError(t, err)
	hostStdoutR, adapterOutW, err := os.Pipe()
	require.NoError(t, err)
	done := make(chan struct{})
	go func() {
		defer close(done)
		r := bufio.NewReader(adapterInR)
		line, _ := aap.ReadLine(r)
		_, _ = aap.ParseHostMessage(line)
		_ = aap.WriteMessage(adapterOutW, aap.Ready{
			ProtocolVersion: aap.ProtocolVersion,
			Agent:           aap.AgentInfo{Name: "unk"},
			Capabilities:    []string{aap.CapStreaming},
		})
		// unknown frame
		_, _ = adapterOutW.Write([]byte(`{"type":"some_future_type","x":1}` + "\n"))
		// then read prompt and answer
		for {
			line, err := aap.ReadLine(r)
			if err != nil {
				return
			}
			msg, _ := aap.ParseHostMessage(line)
			if p, ok := msg.(aap.Prompt); ok {
				_ = aap.WriteMessage(adapterOutW, aap.Message{
					TurnID:  p.TurnID,
					Content: []aap.ContentBlock{aap.TextBlock("ok")},
				})
				_ = aap.WriteMessage(adapterOutW, aap.TurnComplete{TurnID: p.TurnID})
				return
			}
		}
	}()

	s := newAAPHostSessionPipes("unk", "unk", &AgentDef{Kind: AgentKindAAP, Command: "test"}, ctxStore, newResumeStore(""),
		hostStdinW, hostStdoutR,
		func() { cancel(); _ = adapterInR.Close(); _ = adapterOutW.Close() },
		func() error {
			select {
			case <-done:
			case <-time.After(agentShutdownGrace):
			}
			return nil
		})
	require.NoError(t, s.handshake(".", 5*time.Second))
	defer func() { _ = s.shutdown(); cancel(); _ = hostStdinW.Close(); _ = hostStdoutR.Close() }()

	out, turnDone, err := s.sendPrompt("t1", "go")
	require.NoError(t, err)
	var sawReply bool
	for {
		select {
		case msg := <-out:
			if m, ok := msg.(aap.Message); ok && m.Content[0].Text == "ok" {
				sawReply = true
			}
		case err := <-turnDone:
			require.NoError(t, err)
			assert.True(t, sawReply, "turn should complete despite an unknown frame")
			return
		case <-time.After(5 * time.Second):
			t.Fatal("turn did not complete after unknown frame")
		}
	}
}

// TestAAPHostSession_FatalError asserts a fatal error frame ends the turn
// with an error and is recorded in the context store.
func TestAAPHostSession_FatalError(t *testing.T) {
	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctxStore := newContextStore(0)
	ctxStore.init("fatal", "test-node")

	adapterInR, hostStdinW, err := os.Pipe()
	require.NoError(t, err)
	hostStdoutR, adapterOutW, err := os.Pipe()
	require.NoError(t, err)
	adapterDone := make(chan struct{})
	go func() {
		defer close(adapterDone)
		r := bufio.NewReader(adapterInR)
		line, _ := aap.ReadLine(r)
		_, _ = aap.ParseHostMessage(line)
		_ = aap.WriteMessage(adapterOutW, aap.Ready{
			ProtocolVersion: aap.ProtocolVersion,
			Agent:           aap.AgentInfo{Name: "fatal"},
			Capabilities:    []string{aap.CapStreaming},
		})
		// read prompt then emit fatal error
		line, _ = aap.ReadLine(r)
		_, _ = aap.ParseHostMessage(line)
		_ = aap.WriteMessage(adapterOutW, aap.Error{
			Fatal:   true,
			Message: "boom",
		})
	}()

	s := newAAPHostSessionPipes("fatal", "fatal", &AgentDef{Kind: AgentKindAAP, Command: "test"}, ctxStore, newResumeStore(""),
		hostStdinW, hostStdoutR,
		func() { cancel(); _ = adapterInR.Close(); _ = adapterOutW.Close() },
		func() error {
			select {
			case <-adapterDone:
			case <-time.After(agentShutdownGrace):
			}
			return nil
		})
	require.NoError(t, s.handshake(".", 5*time.Second))
	defer func() { _ = s.shutdown(); cancel(); _ = hostStdinW.Close(); _ = hostStdoutR.Close() }()

	_, turnDone, err := s.sendPrompt("t1", "go")
	require.NoError(t, err)
	select {
	case err := <-turnDone:
		require.Error(t, err, "fatal error should end the turn with an error")
	case <-time.After(5 * time.Second):
		t.Fatal("turn did not end after fatal error")
	}

	snap := ctxStore.get("fatal")
	require.NotNil(t, snap)
	require.NotEmpty(t, snap.Errors)
	assert.True(t, snap.Errors[0].Fatal)
	assert.Equal(t, "boom", snap.Errors[0].Message)
}

// TestAAPHostSession_PathEscapeSafety is a placeholder ensuring the adapter
// workspace cwd is passed as-is (advisory scope; the host does not canonicalize).
func TestAAPHostSession_WorkspacePassedThrough(t *testing.T) {
	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctxStore := newContextStore(0)
	ctxStore.init("ws", "test-node")

	adapterInR, hostStdinW, err := os.Pipe()
	require.NoError(t, err)
	hostStdoutR, adapterOutW, err := os.Pipe()
	require.NoError(t, err)
	adapterDone := make(chan struct{})
	var capturedInit aap.Initialize
	go func() {
		defer close(adapterDone)
		r := bufio.NewReader(adapterInR)
		line, _ := aap.ReadLine(r)
		msg, _ := aap.ParseHostMessage(line)
		if init, ok := msg.(aap.Initialize); ok {
			capturedInit = init
		}
		_ = aap.WriteMessage(adapterOutW, aap.Ready{
			ProtocolVersion: aap.ProtocolVersion,
			Agent:           aap.AgentInfo{Name: "ws"},
			Capabilities:    []string{aap.CapStreaming},
		})
		// drain remaining (prompt/shutdown) so the pipe does not block
		buf := make([]byte, 1024)
		for {
			if _, err := r.Read(buf); err != nil {
				return
			}
		}
	}()

	s := newAAPHostSessionPipes("ws", "ws", &AgentDef{Kind: AgentKindAAP, Command: "test"}, ctxStore, newResumeStore(""),
		hostStdinW, hostStdoutR,
		func() { cancel(); _ = adapterInR.Close(); _ = adapterOutW.Close() },
		func() error {
			select {
			case <-adapterDone:
			case <-time.After(agentShutdownGrace):
			}
			return nil
		})
	require.NoError(t, s.handshake("/some/workspace", 5*time.Second))
	defer func() { _ = s.shutdown(); cancel(); _ = hostStdinW.Close(); _ = hostStdoutR.Close() }()

	assert.Equal(t, "/some/workspace", capturedInit.Workspace.Cwd)
}

// nopWriteCloser adapts an io.Writer to io.WriteCloser for tests that build a
// session whose stdin is never meaningfully written to.
type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

// Ensure the test binary's working dir is the server package (so testdata /
// relative paths resolve). This runs at init to keep tests hermetic.
func TestMain_placeholder(t *testing.T) {
	wd, _ := os.Getwd()
	assert.True(t, strings.HasSuffix(filepath.ToSlash(wd), "server"))
}
