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
	err      error // terminal turn error (nil for a clean turn_complete)
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

// finish marks the invocation complete with its terminal error (nil for a
// clean turn_complete) and wakes any readers blocked on done.
func (inv *aapInvocation) finish(err error) {
	inv.mu.Lock()
	if !inv.finished {
		inv.finished = true
		inv.err = err
		close(inv.done)
	}
	inv.mu.Unlock()
}

func (inv *aapInvocation) isFinished() bool {
	inv.mu.Lock()
	defer inv.mu.Unlock()
	return inv.finished
}

// result returns the terminal turn error once the invocation is finished.
func (inv *aapInvocation) result() error {
	inv.mu.Lock()
	defer inv.mu.Unlock()
	return inv.err
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

	if created {
		// New invocation: drive the turn on a context independent of this
		// request, so a client disconnecting (or, in the TUI, leaving the
		// invoke view to approve a tool call) does not cancel the turn. The
		// turn is bounded by the agent lifetime: StopAgent kills the session,
		// closing the frame channels and ending runAAPTurn.
		//nolint:gosec // G118: using a request-independent context is the point — the turn must outlive any single client's request.
		go s.runAAPTurn(context.Background(), session, inv, turnID, invocationID, message)
	}

	// Every client — the primary caller and any reconnecting one — reads the
	// invocation the same way: replay the buffer, then tail live. The request
	// ctx bounds this reader only; canceling it stops streaming to this
	// client, not the turn.
	go s.streamAAPInvocation(ctx, inv, evCh, errCh, 0)
	return evCh, errCh
}

// runAAPTurn drives one AAP turn: it sends the prompt and appends the adapter's
// turn frames to the invocation buffer as SSE-shaped events, then records the
// terminal result. It does not stream to any client — readers tail the buffer
// via streamAAPInvocation, so the turn's lifetime is independent of any client.
//
// turnCtx is a turn-scoped context (not a request): it is canceled on server /
// agent shutdown, which also kills the session and closes the frame channels.
func (s *Server) runAAPTurn(turnCtx context.Context, session *aapHostSession, inv *aapInvocation, turnID, invocationID, message string) {
	// Announcement event (mirrors the ADK "invocation" event shape: an id +
	// agent name so the client can correlate).
	ann, _ := json.Marshal(map[string]string{
		keyInvocationID: invocationID,
		"agent":         session.name,
	})
	inv.add("invocation", ann)

	out, done, err := session.sendPrompt(turnID, message)
	if err != nil {
		s.addAAPError(inv, invocationID, err)
		inv.finish(err)
		return
	}

	// Cancel the turn if the turn context ends (server/agent shutdown), never
	// on a client disconnect.
	pumpCtx, cancel := context.WithCancel(turnCtx)
	defer cancel()
	go func() {
		<-pumpCtx.Done()
		session.cancel(turnID)
	}()

	// Pump turn frames into the buffer until turn_complete (done closed). Each
	// message/tool_call becomes a "token" event; the terminal "done"/"error"
	// event is synthesized here to keep event ordering consistent.
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
				s.addAAPError(inv, invocationID, err)
			} else {
				doneData, _ := json.Marshal(map[string]string{keyInvocationID: invocationID})
				inv.add("done", doneData)
			}
			inv.finish(err)
			return
		}
	}
}

// addAAPError appends a terminal "error" event to the invocation buffer.
func (s *Server) addAAPError(inv *aapInvocation, invocationID string, err error) {
	errData, _ := json.Marshal(map[string]string{
		keyInvocationID: invocationID,
		"error":         err.Error(),
	})
	inv.add("error", errData)
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

// streamAAPInvocation is the single reader path for every client of an
// invocation — the primary caller and any reconnecting one. It subscribes,
// replays buffered events after startID, then tails live events until the
// invocation finishes, and finally delivers the terminal result on errCh.
//
// It keeps a per-reader cursor (the highest event id streamed), so concurrent
// readers and reconnects are each served correctly without re-sending the
// whole buffer. ctx bounds this reader only: canceling it (client disconnect)
// stops streaming to this client, not the turn.
func (s *Server) streamAAPInvocation(ctx context.Context, inv *aapInvocation, evCh chan<- AAPStreamEvent, errCh chan<- error, startID int) {
	defer close(evCh)
	defer close(errCh)

	// Subscribe before the first drain so no event added between the drain and
	// the subscription is missed.
	notify, unsubscribe := inv.subscribe()
	defer unsubscribe()

	cursor := startID
	for {
		cursor = drainAAPEvents(ctx, inv, evCh, cursor)
		if ctx.Err() != nil {
			return
		}
		if inv.isFinished() {
			errCh <- inv.result()
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-notify:
		case <-inv.done:
		}
	}
}

// drainAAPEvents streams buffered events with id > cursor into evCh in order,
// returning the new cursor (the last id streamed, or the input cursor if none).
// It stops early if ctx is canceled.
func drainAAPEvents(ctx context.Context, inv *aapInvocation, evCh chan<- AAPStreamEvent, cursor int) int {
	for _, ev := range inv.eventsAfter(cursor) {
		select {
		case <-ctx.Done():
			return cursor
		case evCh <- AAPStreamEvent{Typ: ev.typ, Data: ev.data}:
			cursor = ev.id
		}
	}
	return cursor
}
