package agentapi

import (
	"context"
	"sync"
)

// bufferedEvent is one event stored in the ring buffer.
type bufferedEvent struct {
	id   int    // sequential SSE event id
	typ  string // SSE event type (e.g. "token", "done", "error")
	data []byte // pre-marshaled JSON payload
}

// invocation holds the state of a single agent run: a ring buffer of events,
// a subscriber list for new-event notifications, and a done channel.
type invocation struct {
	mu       sync.Mutex
	events   []bufferedEvent
	nextID   int
	done     chan struct{}
	finished bool
	subs     map[*subscriber]struct{}
}

// subscriber receives a notification (channel send) when a new event is
// added to the invocation's buffer.
type subscriber struct {
	ch chan struct{}
}

// add appends an event to the ring buffer. If the buffer exceeds bufferCap,
// the oldest events are dropped. Subscribers are notified after the event
// is added.
func (inv *invocation) add(typ string, data []byte) {
	inv.mu.Lock()
	inv.nextID++
	id := inv.nextID
	inv.events = append(inv.events, bufferedEvent{id: id, typ: typ, data: data})
	if len(inv.events) > bufferCap {
		inv.events = inv.events[len(inv.events)-bufferCap:]
	}
	subs := make([]*subscriber, 0, len(inv.subs))
	for s := range inv.subs {
		subs = append(subs, s)
	}
	inv.mu.Unlock()

	// Notify subscribers outside the lock to avoid blocking add on a slow
	// subscriber. Non-blocking: if the subscriber's channel is full it
	// misses the notification but will still drain the buffer on the next
	// one.
	for _, s := range subs {
		select {
		case s.ch <- struct{}{}:
		default:
		}
	}
}

// eventsAfter returns all buffered events with ids greater than lastID,
// preserving order.
func (inv *invocation) eventsAfter(lastID int) []bufferedEvent {
	inv.mu.Lock()
	defer inv.mu.Unlock()
	var out []bufferedEvent
	for _, ev := range inv.events {
		if ev.id > lastID {
			out = append(out, ev)
		}
	}
	return out
}

// subscribe registers a new subscriber for new-event notifications.
func (inv *invocation) subscribe() *subscriber {
	inv.mu.Lock()
	defer inv.mu.Unlock()
	s := &subscriber{ch: make(chan struct{}, 1)}
	if inv.subs == nil {
		inv.subs = make(map[*subscriber]struct{})
	}
	inv.subs[s] = struct{}{}
	return s
}

// unsubscribe removes a subscriber.
func (inv *invocation) unsubscribe(s *subscriber) {
	inv.mu.Lock()
	defer inv.mu.Unlock()
	delete(inv.subs, s)
}

// markFinished closes the done channel. Idempotent.
func (inv *invocation) markFinished() {
	inv.mu.Lock()
	if !inv.finished {
		inv.finished = true
		close(inv.done)
	}
	inv.mu.Unlock()
}

// isFinished reports whether the invocation's run has completed.
func (inv *invocation) isFinished() bool {
	inv.mu.Lock()
	defer inv.mu.Unlock()
	return inv.finished
}

// invocationRegistry tracks all active and recently-finished invocations
// for an agent subprocess.
type invocationRegistry struct {
	mu          sync.Mutex
	invocations map[string]*invocation
}

func newInvocationRegistry() *invocationRegistry {
	return &invocationRegistry{
		invocations: make(map[string]*invocation),
	}
}

// getOrCreate returns the invocation for id, creating a new one if it does
// not exist. The created bool is true when a new invocation was created.
func (r *invocationRegistry) getOrCreate(id string) (*invocation, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	inv, ok := r.invocations[id]
	if !ok {
		inv = &invocation{done: make(chan struct{})}
		r.invocations[id] = inv
	}
	return inv, !ok
}

// httpBackgroundContext returns a context that is not canceled when the
// HTTP request ends. The agent run's lifetime is tied to the invocation,
// not the request. A process-level cancellation (SIGINT/SIGTERM) should
// be layered on top by the caller.
func httpBackgroundContext() context.Context {
	return context.Background()
}
