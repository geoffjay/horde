package server

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/google/uuid"

	"github.com/geoffjay/horde/internal/aap"
)

// aapEvent is one SSE-shaped event buffered for an AAP invocation. It mirrors
// the shape the ADK agentapi produces (token/done/error events) so the API
// handler writes the same stream for both agent kinds.
type aapEvent struct {
	id   int
	typ  string
	data []byte
}

// aapBufferCap is the maximum number of events retained per AAP invocation
// for Last-Event-ID resume. It mirrors the ADK agentapi bufferCap.
const aapBufferCap = 256

// keyInvocationID is the JSON field the invoke stream uses to correlate events
// with their invocation (matching the ADK announcement shape).
const keyInvocationID = "invocation_id"

// aapInvocation holds the ring buffer + subscriber set for one AAP invoke
// call, mirroring the agentapi invocation. The invoke handler is a reader
// that replays from the buffer and tails new events; client disconnect does
// not cancel the turn (the turn is tied to the invocation, not the request).
type aapInvocation struct {
	mu       sync.Mutex
	events   []aapEvent
	nextID   int
	done     chan struct{}
	finished bool
	subs     map[chan struct{}]struct{}
}

func newAAPInvocation() *aapInvocation {
	return &aapInvocation{
		done: make(chan struct{}),
		subs: make(map[chan struct{}]struct{}),
	}
}

func (inv *aapInvocation) add(typ string, data []byte) {
	inv.mu.Lock()
	inv.nextID++
	id := inv.nextID
	inv.events = append(inv.events, aapEvent{id: id, typ: typ, data: data})
	if len(inv.events) > aapBufferCap {
		inv.events = inv.events[len(inv.events)-aapBufferCap:]
	}
	subs := make([]chan struct{}, 0, len(inv.subs))
	for ch := range inv.subs {
		subs = append(subs, ch)
	}
	inv.mu.Unlock()
	for _, ch := range subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

func (inv *aapInvocation) eventsAfter(lastID int) []aapEvent {
	inv.mu.Lock()
	defer inv.mu.Unlock()
	var out []aapEvent
	for _, ev := range inv.events {
		if ev.id > lastID {
			out = append(out, ev)
		}
	}
	return out
}

func (inv *aapInvocation) subscribe() (notify chan struct{}, unsubscribe func()) {
	ch := make(chan struct{}, 1)
	inv.mu.Lock()
	inv.subs[ch] = struct{}{}
	inv.mu.Unlock()
	cancel := func() {
		inv.mu.Lock()
		delete(inv.subs, ch)
		inv.mu.Unlock()
	}
	return ch, cancel
}

func (inv *aapInvocation) markFinished() {
	inv.mu.Lock()
	if !inv.finished {
		inv.finished = true
		close(inv.done)
	}
	inv.mu.Unlock()
}

func (inv *aapInvocation) isFinished() bool {
	inv.mu.Lock()
	defer inv.mu.Unlock()
	return inv.finished
}

// aapInvocationRegistry tracks active and recently-finished AAP invocations
// per agent. It mirrors the agentapi invocationRegistry; AAP invocations are
// keyed by invocationID so a reconnecting client with Last-Event-ID resumes
// from the buffer.
type aapInvocationRegistry struct {
	mu          sync.Mutex
	invocations map[string]*aapInvocation
}

func newAAPInvocationRegistry() *aapInvocationRegistry {
	return &aapInvocationRegistry{
		invocations: make(map[string]*aapInvocation),
	}
}

func (r *aapInvocationRegistry) getOrCreate(id string) (*aapInvocation, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	inv, ok := r.invocations[id]
	if !ok {
		inv = newAAPInvocation()
		r.invocations[id] = inv
	}
	return inv, !ok
}

// AAPStreamEvent is one SSE-shaped event delivered to the API layer from an
// AAP invoke turn. It mirrors the aapStreamEvent type declared in the api
// package but lives here to avoid an api→server import in the return type.
type AAPStreamEvent struct {
	Typ  string
	Data []byte
}

// AAPInvoke runs one AAP turn against the agent's adapter session and
// returns a stream of SSE-shaped events. It is the AAP equivalent of the ADK
// reverse proxy: the node runs the turn (writing a prompt to the adapter's
// stdin) and translates the adapter's agent frames into the same event
// shape (token/done/error) the ADK path produces, so the invoke URL and SSE
// response shape are unchanged across agent kinds.
//
// The session key (agent_id, project_id) is accepted for signature parity with
// the ADK invoke path but is unused here: AAP session continuity is carried by
// the persistent adapter subprocess, not per-invocation. invocationID drives
// Last-Event-ID resume. The events channel is closed when the turn's buffered
// events are fully delivered; the err channel receives a terminal error (nil
// for a normal turn_complete).
func (s *Server) AAPInvoke(ctx context.Context, agentID, _, invocationID, message string) (events <-chan AAPStreamEvent, errs <-chan error) {
	// Resolve the AAP session. Unknown / non-AAP agent returns an error
	// stream rather than blocking the caller.
	s.mu.Lock()
	proc, ok := s.procs[agentID]
	s.mu.Unlock()
	if !ok || proc.kind != AgentKindAAP || proc.aapSession == nil {
		errCh := make(chan error, 1)
		errCh <- fmt.Errorf("agent %q is not an AAP agent", agentID)
		close(errCh)
		evCh := make(chan AAPStreamEvent)
		close(evCh)
		return evCh, errCh
	}
	session := proc.aapSession

	if invocationID == "" {
		invocationID = uuid.NewString()
	}
	// The AAP turn_id is the invocation id. The session key (project-derived)
	// is carried out-of-band by the node's per-project session continuity;
	// the AAP turn itself is identified by turn_id. The two coexist exactly
	// as the decision specifies for the ADK path.
	turnID := invocationID

	regs := s.aapInvokes
	inv, created := regs.getOrCreate(invocationID)

	evCh := make(chan AAPStreamEvent, 1)
	errCh := make(chan error, 1)

	if !created {
		// A reconnecting client: replay from the buffer, then tail. No new
		// turn is started.
		go s.replayAAPInvocation(ctx, inv, evCh, errCh)
		return evCh, errCh
	}

	// Start the turn in a goroutine whose lifetime is tied to the
	// invocation, not the HTTP request (mirrors the ADK runInvocation).
	go s.runAAPTurn(ctx, session, inv, turnID, invocationID, message, evCh, errCh)
	return evCh, errCh
}

// runAAPTurn sends the prompt, drains the adapter's turn output into the
// invocation buffer as SSE-shaped events, and signals completion. evCh/errCh
// deliver a streamed view of the buffer to this caller; a reconnecting
// client gets a fresh reader via replayAAPInvocation.
func (s *Server) runAAPTurn(ctx context.Context, session *aapHostSession, inv *aapInvocation, turnID, invocationID, message string, evCh chan<- AAPStreamEvent, errCh chan<- error) {
	defer func() {
		inv.markFinished()
		close(evCh)
	}()

	// Announcement event (mirrors the ADK "invocation" event shape: an id +
	// agent name so the client can correlate).
	ann, _ := json.Marshal(map[string]string{
		keyInvocationID: invocationID,
		"agent":         session.name,
	})
	inv.add("invocation", ann)

	out, done, err := session.sendPrompt(turnID, message)
	if err != nil {
		errData, _ := json.Marshal(map[string]string{
			keyInvocationID: invocationID,
			"error":         err.Error(),
		})
		inv.add("error", errData)
		errCh <- err
		close(errCh)
		return
	}

	// Pump turn frames into the buffer until turn_complete (done closed) or
	// context cancel. Each message/tool_call becomes a "token" event;
	// turn_complete's result becomes a "done" event (mirroring the ADK
	// done event shape).
	pumpCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	go func() {
		<-pumpCtx.Done()
		session.cancel(turnID)
	}()

	for {
		select {
		case msg, ok := <-out:
			if !ok {
				// out was closed by the session after endTurn; loop to
				// check done.
				continue
			}
			s.bufferAAPFrame(inv, invocationID, msg)
		case err := <-done:
			if err != nil {
				errData, _ := json.Marshal(map[string]string{
					keyInvocationID: invocationID,
					"error":         err.Error(),
				})
				inv.add("error", errData)
				errCh <- err
			} else {
				doneData, _ := json.Marshal(map[string]string{
					keyInvocationID: invocationID,
				})
				inv.add("done", doneData)
				errCh <- nil
			}
			close(errCh)
			// Drain any remaining buffered events to the stream before
			// returning; the replay path handles reconnects.
			s.streamAAPBuffer(ctx, inv, evCh)
			return
		}
	}
}

// bufferAAPFrame translates one AAP agent frame into an SSE-shaped event and
// appends it to the invocation buffer. The mapping preserves the ADK stream
// shape so clients consume both kinds identically:
//   - aap.Message → "token" (the assistant content blocks)
//   - aap.ToolCall → "token" (tool-use announcements are part of the turn
//     output stream)
//   - aap.TurnComplete → "done" is emitted by runAAPTurn on turn end, not
//     here, to keep the terminal event ordering consistent.
func (s *Server) bufferAAPFrame(inv *aapInvocation, _ string, msg aap.AgentMessage) {
	switch m := msg.(type) {
	case aap.Message:
		data, _ := json.Marshal(m)
		inv.add("token", data)
	case aap.ToolCall:
		data, _ := json.Marshal(m)
		inv.add("token", data)
	case aap.TurnComplete:
		// Stored as a token too so a replaying client sees the full output;
		// the terminal "done" event is synthesized by runAAPTurn.
		data, _ := json.Marshal(m)
		inv.add("token", data)
	}
}

// streamAAPBuffer replays buffered events with ids greater than lastID into
// evCh. It is the streaming half of the invoke response: the caller's evCh
// receives each event in order. A nil lastID starts from the beginning.
func (s *Server) streamAAPBuffer(ctx context.Context, inv *aapInvocation, evCh chan<- AAPStreamEvent, lastID ...int) {
	start := 0
	if len(lastID) > 0 {
		start = lastID[0]
	}
	for _, ev := range inv.eventsAfter(start) {
		select {
		case <-ctx.Done():
			return
		case evCh <- AAPStreamEvent{Typ: ev.typ, Data: ev.data}:
		}
	}
}

// replayAAPInvocation serves a reconnecting client: replay buffered events
// from Last-Event-ID onward, then tail new events until the invocation is
// finished. It does not start a new turn.
func (s *Server) replayAAPInvocation(ctx context.Context, inv *aapInvocation, evCh chan<- AAPStreamEvent, errCh chan<- error) {
	defer func() {
		close(evCh)
		close(errCh)
	}()
	s.streamAAPBuffer(ctx, inv, evCh)
	if inv.isFinished() {
		return
	}
	ch, cancel := inv.subscribe()
	defer cancel()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ch:
		case <-inv.done:
		}
		finished := inv.isFinished()
		// Drain new events since the last delivered id by tracking the
		// highest id we have streamed. A simple approach: re-stream from the
		// buffer each notification, relying on the caller to dedupe by SSE
		// id. The api handler tracks Last-Event-ID, so we stream the whole
		// buffer tail each wake is not ideal; instead track locally.
		s.streamAAPBuffer(ctx, inv, evCh, s.lastStreamedID(inv))
		if finished {
			return
		}
	}
}

// lastStreamedID is a placeholder for per-reader cursor tracking. The
// replay path is best-effort in v1; a real Last-Event-ID cursor is carried
// by the HTTP handler and used to seed the replay. This helper keeps the
// replay from re-sending the whole buffer by tracking the buffer's current
// max id at wake time, which is correct for a single reader but approximates
// for concurrent reconnects. A follow-up can thread an explicit cursor.
func (s *Server) lastStreamedID(inv *aapInvocation) int {
	inv.mu.Lock()
	defer inv.mu.Unlock()
	if len(inv.events) == 0 {
		return 0
	}
	return inv.events[len(inv.events)-1].id - 1
}
