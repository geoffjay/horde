package server

import (
	"sync"

	"github.com/google/uuid"
)

// Event is a single agent invocation event published on the bus.
type Event struct {
	// InvocationID is the id of the invocation that produced this event.
	InvocationID string
	// Type is the event type (e.g. "token", "log", "done").
	Type string
	// Data is the event payload, already JSON-encodable.
	Data any
}

// EventBus is an in-process, brokerless event bus backed by Go channels.
// Subscribers fan out per invocation id; publishers never block (slow
// subscribers are dropped).
type EventBus struct {
	mu sync.Mutex

	nextSub int
	subs    map[int]*subscription
}

type subscription struct {
	id           int
	invocationID string
	ch           chan Event
}

// NewEventBus constructs an empty EventBus.
func NewEventBus() *EventBus {
	return &EventBus{subs: make(map[int]*subscription)}
}

// Publish delivers ev to every subscriber matching ev.InvocationID. A
// subscriber whose channel is full is dropped (non-blocking) so a slow
// consumer cannot stall a publisher.
func (b *EventBus) Publish(ev Event) {
	b.mu.Lock()
	subs := make([]*subscription, 0, len(b.subs))
	for _, s := range b.subs {
		if s.invocationID == "" || s.invocationID == ev.InvocationID {
			subs = append(subs, s)
		}
	}
	b.mu.Unlock()

	for _, s := range subs {
		select {
		case s.ch <- ev:
		default:
			// drop on slow subscriber
		}
	}
}

// Subscribe returns a receive-only channel of events for the given
// invocation id. An empty invocationID subscribes to all events. The caller
// must call the returned cancel func when done to release the subscription.
func (b *EventBus) Subscribe(invocationID string) (events <-chan Event, cancel func()) {
	b.mu.Lock()
	id := b.nextSub
	b.nextSub++
	ch := make(chan Event, eventBusBufferSize)
	s := &subscription{id: id, invocationID: invocationID, ch: ch}
	b.subs[id] = s
	b.mu.Unlock()

	cancel = func() {
		b.mu.Lock()
		delete(b.subs, id)
		b.mu.Unlock()
		close(ch)
	}
	return ch, cancel
}

// eventBusBufferSize is the per-subscriber channel capacity. Events beyond
// this are dropped (non-blocking) so a slow subscriber cannot stall a
// publisher.
const eventBusBufferSize = 32

// NewInvocationID generates a fresh invocation identifier.
func NewInvocationID() string {
	return uuid.NewString()
}
