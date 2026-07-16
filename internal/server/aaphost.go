package server

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/geoffjay/horde/internal/aap"
)

// aapHostSession is the node-side state machine for one AAP adapter
// subprocess. It owns the duplex stdio pipe to the adapter, runs the
// initialize→ready handshake, and dispatches every agent→host frame the
// adapter emits on stdout. Host→agent frames (prompt, cancel,
// approval_response, shutdown) are written to stdin by the caller methods.
//
// One session maps to one agentProc. The session is single-turn-concurrent:
// prompts are serialized by the caller (the invoke path), but the reader
// goroutine handles all agent output asynchronously, matching the spec's
// full-duplex model (approval_request can arrive mid-turn).
type aapHostSession struct {
	agentID string
	name    string
	def     AgentDef

	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser

	// stop terminates the adapter. For a subprocess it sends SIGINT then
	// SIGKILL; for a test-injected adapter it closes the pipe driving it.
	// wait blocks until the adapter has fully exited. Both are nil-safe
	// (no-ops) when the session was built without a subprocess (test path).
	stop func()
	wait func() error

	// ready is the cached handshake response (capabilities, models). It is
	// set once during the handshake and read-only after.
	ready *aap.Ready

	// ctxStore receives the AAP frames the reader dispatches (status,
	// context, error, approval_request). It is the execution-context store
	// shared with the agentProc.
	ctxStore *contextStore

	// turnOut delivers agent frames belonging to the active turn to the
	// invoke bridge. A turn subscribes before sending its prompt and drains
	// until turn_complete (or a fatal error / ctx cancel). Only one turn is
	// active at a time; the caller serializes prompts.
	turnMu   sync.Mutex
	turnOut  chan aap.AgentMessage
	turnDone chan error // closed when the active turn's turn_complete arrives or the turn fails
	turnID   string

	// pendingApprovals tracks request_id → decision channels. The approval
	// policy resolves a decision when auto_approve is set; otherwise the
	// request stays pending until an external decision (follow-up) or turn
	// end clears it.
	approvalsMu      sync.Mutex
	pendingApprovals map[string]chan aap.ApprovalDecision

	// doneCh is closed when the subprocess exits (reader goroutine sees EOF
	// or the cmd Wait completes). trackAgentExit also closes the agentProc
	// doneCh; this is the AAP-specific signal.
	doneCh chan struct{}
}

// newAAPHostSession spawns the adapter subprocess but does not run the
// handshake; the caller runs handshake() to complete initialization. The
// workspace is resolved by the caller and passed to handshake, not here.
func newAAPHostSession(ctx context.Context, agentID, name string, def *AgentDef, _ string, ctxStore *contextStore) (*aapHostSession, func(), error) {
	if def.Command == "" {
		return nil, nil, fmt.Errorf("aap agent %q has no command", name)
	}

	cmdCtx, cancel := context.WithCancel(context.Background())
	args := make([]string, 0, len(def.Args))
	args = append(args, def.Args...)
	// Adapter command is operator-controlled config, not untrusted input.
	cmd := exec.CommandContext(cmdCtx, def.Command, args...) //#nosec G204
	cmd.Env = aapAdapterEnv(def.Env)
	cmd.Stderr = os.Stderr

	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, nil, fmt.Errorf("create stdin pipe for aap agent %q: %w", name, err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, nil, fmt.Errorf("create stdout pipe for aap agent %q: %w", name, err)
	}
	cmd.Cancel = func() error {
		_ = cmd.Process.Signal(os.Interrupt)
		return nil
	}
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, nil, fmt.Errorf("start aap agent %q: %w", name, err)
	}

	s := &aapHostSession{
		agentID:          agentID,
		name:             name,
		def:              *def,
		cmd:              cmd,
		stdin:            stdin,
		stdout:           stdout,
		ctxStore:         ctxStore,
		pendingApprovals: make(map[string]chan aap.ApprovalDecision),
		doneCh:           make(chan struct{}),
		stop:             func() { _ = cmd.Process.Signal(os.Interrupt) },
		wait:             func() error { _ = cmd.Wait(); return nil },
	}

	// Wire the subprocess lifecycle: if the caller's ctx is canceled, tear
	// the adapter down. The caller also calls shutdown() on StopAgent.
	go func() {
		select {
		case <-ctx.Done():
			_ = s.shutdown()
		case <-s.doneCh:
		}
	}()

	return s, cancel, nil
}

// newAAPHostSessionPipes builds a session from pre-made stdin/stdout and
// stop/wait funcs, for tests that drive an in-process adapter over io.Pipe
// pairs (no subprocess). The handshake + reader goroutine run identically.
//
//nolint:unused // test-only constructor; golangci-lint has run.tests:false so its callers in _test.go don't count
func newAAPHostSessionPipes(agentID, name string, def *AgentDef, ctxStore *contextStore, stdin io.WriteCloser, stdout io.Reader, stop func(), wait func() error) *aapHostSession {
	return &aapHostSession{
		agentID:          agentID,
		name:             name,
		def:              *def,
		stdin:            stdin,
		stdout:           io.NopCloser(stdout),
		ctxStore:         ctxStore,
		pendingApprovals: make(map[string]chan aap.ApprovalDecision),
		doneCh:           make(chan struct{}),
		stop:             stop,
		wait:             wait,
	}
}

// aapAdapterEnv builds the environment for an AAP adapter subprocess: the
// current process environment, the AAP transport variables (canonical +
// legacy alias), and the configured extras. The host forces stdio; an
// operator-configured AAP_TRANSPORT is overridden because the horde host
// only implements the stdio binding in v1.
func aapAdapterEnv(extra []EnvPair) []string {
	env := os.Environ()
	set := func(k, v string) {
		env = append(env, k+"="+v)
	}
	set(aap.EnvTransport, aap.TransportStdio)
	set(aap.LegacyEnvTransport, aap.TransportStdio)
	for _, p := range extra {
		set(p.Key, p.Value)
	}
	return env
}

// readReadyFrame reads agent frames until it finds ready or error, skipping
// diagnostic log frames and unknown-type frames that may legitimately precede
// ready (spec §3, §logging). It returns the ready/error message, or an error
// on a read/parse failure.
func (s *aapHostSession) readReadyFrame(r *bufio.Reader) (aap.AgentMessage, error) {
	for {
		line, err := aap.ReadLine(r)
		if err != nil {
			return nil, fmt.Errorf("read ready: %w", err)
		}
		msg, perr := aap.ParseAgentMessage(line)
		if perr != nil {
			var unknown *aap.UnknownTypeError
			if errors.As(perr, &unknown) {
				continue // unknown frame before ready — skip it
			}
			return nil, fmt.Errorf("parse ready: %w", perr)
		}
		if lg, ok := msg.(aap.Log); ok {
			// Diagnostic log emitted before ready; surface it and keep waiting.
			logrus.WithField(logKeyAgent, s.name).Debugf("aap: pre-ready log: %s", lg.Message)
			continue
		}
		return msg, nil
	}
}

// handshake sends initialize and waits for ready. On success the reader
// goroutine is started. On any failure the subprocess is torn down.
func (s *aapHostSession) handshake(workspace string, timeout time.Duration) error {
	init := aap.Initialize{
		ProtocolVersion: aap.ProtocolVersion,
		Workspace:       aap.Workspace{Cwd: workspace},
	}
	if s.def.Model != "" {
		m := s.def.Model
		init.Model = &m
	}
	if s.def.SystemPrompt != "" {
		mode := aap.SystemPromptReplace
		if s.def.SystemPromptMode == string(aap.SystemPromptAppend) {
			mode = aap.SystemPromptAppend
		}
		text := s.def.SystemPrompt
		init.SystemPrompt = &aap.SystemPrompt{Mode: mode, Text: &text}
	}
	if s.def.Permissions != nil {
		init.Permissions = &aap.Permissions{
			Mode:          aap.PermissionMode(s.def.Permissions.Mode),
			WritablePaths: s.def.Permissions.WritablePaths,
			DenyPaths:     s.def.Permissions.DenyPaths,
		}
	}

	if err := aap.WriteMessage(s.stdin, init); err != nil {
		_ = s.kill()
		return fmt.Errorf("aap %q: write initialize: %w", s.name, err)
	}

	// Read agent frames with a timeout until ready (or a fatal error). An
	// adapter may emit diagnostic log frames (and, per spec §3, unknown-type
	// frames) before ready; readReadyFrame skips those.
	type result struct {
		msg aap.AgentMessage
		err error
	}
	ch := make(chan result, 1)
	r := bufio.NewReader(s.stdout)
	go func() {
		msg, err := s.readReadyFrame(r)
		ch <- result{msg, err}
	}()

	select {
	case res := <-ch:
		if res.err != nil {
			_ = s.kill()
			return res.err
		}
		switch m := res.msg.(type) {
		case aap.Ready:
			if m.ProtocolVersion != aap.ProtocolVersion {
				_ = s.kill()
				return fmt.Errorf("aap %q: protocol version mismatch: adapter speaks %d, host speaks %d",
					s.name, m.ProtocolVersion, aap.ProtocolVersion)
			}
			s.ready = &m
		case aap.Error:
			_ = s.kill()
			return fmt.Errorf("aap %q: adapter fatal error: %s", s.name, m.Message)
		default:
			_ = s.kill()
			return fmt.Errorf("aap %q: expected ready or error, got %s", s.name, m.Type())
		}
	case <-time.After(timeout):
		_ = s.kill()
		return fmt.Errorf("aap %q: ready handshake timed out", s.name)
	}

	// Start the reader goroutine, reusing the handshake's buffered reader so
	// any frame already buffered after ready is not lost. The reader owns
	// stdout from here.
	go s.readLoop(r)
	return nil
}

// readLoop reads NDJSON frames from stdout and dispatches them. It runs until
// stdout reaches EOF (the adapter exited) or a read error. Each frame is
// parsed as an AgentMessage and routed: turn output to the active turn's
// channel, context/status/error/approval to the ctxStore and approval policy.
func (s *aapHostSession) readLoop(r *bufio.Reader) {
	defer close(s.doneCh)
	for {
		line, err := aap.ReadLine(r)
		if err != nil {
			if !errors.Is(err, io.EOF) {
				logrus.WithError(err).WithField(logKeyAgent, s.name).Warn("aap reader: read error")
			}
			// Signal any active turn that the stream ended.
			s.endTurn(fmt.Errorf("adapter stream ended: %w", err))
			return
		}
		msg, perr := aap.ParseAgentMessage(line)
		if perr != nil {
			var unknown *aap.UnknownTypeError
			if errors.As(perr, &unknown) {
				logrus.WithField(logKeyAgent, s.name).WithField("type", unknown.Type).Debug("aap: skipping unknown frame")
				continue
			}
			logrus.WithError(perr).WithField(logKeyAgent, s.name).Warn("aap: parse error")
			continue
		}
		s.dispatch(msg)
	}
}

// dispatch routes one agent frame to its destination.
func (s *aapHostSession) dispatch(msg aap.AgentMessage) {
	switch m := msg.(type) {
	case aap.Message, aap.ToolCall:
		s.deliverTurn(msg)
	case aap.TurnComplete:
		s.deliverTurn(msg)
		s.endTurn(nil)
	case aap.Status:
		s.ctxStore.applyStatus(s.agentID, m)
	case aap.ContextUpdate:
		s.ctxStore.applyContextUpdate(s.agentID, m)
	case aap.Error:
		s.ctxStore.applyError(s.agentID, m)
		if m.Fatal {
			logrus.WithField(logKeyAgent, s.name).WithField("message", m.Message).Warn("aap adapter fatal error")
			s.endTurn(fmt.Errorf("adapter fatal error: %s", m.Message))
			// The reader will see EOF next; the fatal flag also informs
			// the lifecycle via ctxStore.setLifecycle in trackAgentExit.
		}
	case aap.ApprovalRequest:
		s.ctxStore.applyApprovalRequest(s.agentID, m)
		s.resolveApproval(m)
	case aap.Log:
		level := logrus.InfoLevel
		switch m.Level {
		case aap.LogWarn:
			level = logrus.WarnLevel
		case aap.LogError:
			level = logrus.ErrorLevel
		}
		logrus.WithField(logKeyAgent, s.name).Logf(level, "aap: %s", m.Message)
	default:
		logrus.WithField(logKeyAgent, s.name).WithField("type", msg.Type()).Debug("aap: unhandled frame")
	}
}

// deliverTurn sends a frame to the active turn's output channel. If no turn
// is active the frame is dropped (a stray message between turns).
func (s *aapHostSession) deliverTurn(msg aap.AgentMessage) {
	s.turnMu.Lock()
	out := s.turnOut
	s.turnMu.Unlock()
	if out == nil {
		return
	}
	select {
	case out <- msg:
	default:
		// The turn channel is buffered to 1; a full buffer means the invoke
		// reader is slow. Drop the oldest to coalesce, but log so it is
		// visible.
		logrus.WithField(logKeyAgent, s.name).WithField("type", msg.Type()).Debug("aap: turn channel full, coalescing")
		select {
		case <-out:
		default:
		}
		select {
		case out <- msg:
		default:
		}
	}
}

// startTurn begins a new turn, returning the channel the invoke bridge
// drains. Only one turn is active at a time; the caller serializes prompts.
// turnDone is closed when the turn completes (turn_complete) or fails.
func (s *aapHostSession) startTurn(turnID string) (frames <-chan aap.AgentMessage, done <-chan error) {
	out := make(chan aap.AgentMessage, 1)
	doneCh := make(chan error, 1)
	s.turnMu.Lock()
	s.turnOut = out
	s.turnDone = doneCh
	s.turnID = turnID
	s.turnMu.Unlock()
	return out, doneCh
}

// endTurn closes the active turn's done channel with err (nil for a normal
// turn_complete) and clears the turn state. It is idempotent: a second
// endTurn (e.g. stream EOF after turn_complete) is a no-op.
func (s *aapHostSession) endTurn(err error) {
	s.turnMu.Lock()
	done := s.turnDone
	out := s.turnOut
	s.turnDone = nil
	s.turnOut = nil
	s.turnID = ""
	s.turnMu.Unlock()
	if done == nil {
		return
	}
	// Clear pending approvals for this turn: any unresolved requests are
	// moot once the turn ends.
	s.clearPendingApprovals()
	select {
	case done <- err:
	default:
		if err != nil {
			done <- err
		}
	}
	close(done)
	if out != nil {
		// Drain any buffered frame so the invoke reader does not block on a
		// stale value; the done signal is the terminal event.
		select {
		case <-out:
		default:
		}
	}
}

// sendPrompt writes a prompt frame and returns the turn output channel. The
// caller derives the session key above this layer (the project binding) and
// uses turnID as the AAP turn_id.
func (s *aapHostSession) sendPrompt(turnID, message string) (frames <-chan aap.AgentMessage, done <-chan error, err error) {
	out, doneCh := s.startTurn(turnID)
	if err := aap.WriteMessage(s.stdin, aap.Prompt{
		TurnID:  turnID,
		Content: aap.TextPrompt(message),
	}); err != nil {
		s.endTurn(err)
		return nil, nil, fmt.Errorf("aap %q: write prompt: %w", s.name, err)
	}
	return out, doneCh, nil
}

// cancel writes a cancel frame for the given turn (or the active turn when
// turnID is empty). Best-effort; the adapter may not advertise cancel.
func (s *aapHostSession) cancel(turnID string) {
	msg := aap.Cancel{}
	if turnID != "" {
		msg.TurnID = &turnID
	}
	_ = aap.WriteMessage(s.stdin, msg)
}

// resolveApproval applies the approval policy to an approval_request. With
// auto_approve the host immediately allows the call and clears the pending
// ref; otherwise the request stays pending (recorded in the ctxStore) until
// an external decision arrives via respondApproval or the turn ends.
func (s *aapHostSession) resolveApproval(req aap.ApprovalRequest) {
	if !s.def.AutoApprove {
		// Record a pending decision channel so a future respondApproval can
		// resolve it; no auto-decision is made.
		ch := make(chan aap.ApprovalDecision, 1)
		s.approvalsMu.Lock()
		s.pendingApprovals[req.RequestID] = ch
		s.approvalsMu.Unlock()
		return
	}
	s.respondApproval(req.RequestID, aap.DecisionAllow)
}

// resolvePending resolves a pending approval by request id with an explicit
// decision — the external (human/API) decision path. Unlike respondApproval it
// guards against a request id that is not currently pending (unknown, already
// resolved, or cleared when the turn ended), returning ErrApprovalNotFound so
// the caller can map it to a 404 rather than silently writing a stray response.
func (s *aapHostSession) resolvePending(requestID string, decision aap.ApprovalDecision) error {
	s.approvalsMu.Lock()
	_, ok := s.pendingApprovals[requestID]
	s.approvalsMu.Unlock()
	if !ok {
		return ErrApprovalNotFound
	}
	s.respondApproval(requestID, decision)
	return nil
}

// respondApproval sends an approval_response and clears the pending ref. It
// is the single path that writes the response, used by the auto-approve policy
// and by resolvePending (the external decision path).
func (s *aapHostSession) respondApproval(requestID string, decision aap.ApprovalDecision) {
	resp := aap.ApprovalResponse{RequestID: requestID, Decision: decision}
	if err := aap.WriteMessage(s.stdin, resp); err != nil {
		logrus.WithError(err).WithField(logKeyAgent, s.name).Warn("aap: write approval response")
	}
	s.ctxStore.applyApprovalResponse(s.agentID, requestID)
	s.approvalsMu.Lock()
	delete(s.pendingApprovals, requestID)
	s.approvalsMu.Unlock()
}

// clearPendingApprovals drops all unresolved approval channels for the active
// turn. The ctxStore entries remain (they are cleared on the next
// applyApprovalResponse or by setLifecycle on exit); this just stops the
// host from holding decision channels for a turn that is over.
func (s *aapHostSession) clearPendingApprovals() {
	s.approvalsMu.Lock()
	s.pendingApprovals = make(map[string]chan aap.ApprovalDecision)
	s.approvalsMu.Unlock()
}

// shutdown writes a shutdown frame, waits for the process to exit within
// agentShutdownGrace, then force-kills if needed. It is safe to call
// multiple times. For a test-injected session (no subprocess) it closes the
// stop func and waits for the reader to see EOF.
func (s *aapHostSession) shutdown() error {
	_ = aap.WriteMessage(s.stdin, aap.Shutdown{})
	return s.waitOrKill()
}

// kill force-kills the subprocess without a shutdown frame. Used on
// handshake failure. For a test-injected session it calls stop.
func (s *aapHostSession) kill() error {
	if s.stop != nil {
		s.stop()
	}
	if s.cmd != nil && s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
	}
	return s.waitOrKill()
}

// waitOrKill waits for the adapter to exit within agentShutdownGrace, then
// force-stops it. It does not close stdin/stdout (the reader goroutine owns
// stdout via doneCh); trackAgentExit calls wait for the subprocess path.
func (s *aapHostSession) waitOrKill() error {
	if s.wait == nil {
		return nil
	}
	waitCh := make(chan struct{})
	go func() { _ = s.wait(); close(waitCh) }()
	select {
	case <-waitCh:
		return nil
	case <-time.After(agentShutdownGrace):
		if s.stop != nil {
			s.stop()
		}
		if s.cmd != nil && s.cmd.Process != nil {
			_ = s.cmd.Process.Kill()
		}
		<-waitCh
		return errors.New("aap adapter did not exit, force-killed")
	}
}

// hasCapability reports whether the adapter advertised a capability token.
//
//nolint:unused // exercised by _test.go; golangci-lint has run.tests:false so those callers don't count
func (s *aapHostSession) hasCapability(token string) bool {
	if s.ready == nil {
		return false
	}
	for _, c := range s.ready.Capabilities {
		if c == token {
			return true
		}
	}
	return false
}
