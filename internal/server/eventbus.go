package server

import "sync"

// Event is a discrete cluster-activity event: an agent lifecycle transition on
// some node. Events fan out over the node's EventBus. In a cluster, slaves
// forward their events to the master (POST /api/v1/cluster/events), which
// republishes them, so the master's /events/stream is a cluster-wide feed.
// Events carry no sensitive payload — an agent id, the origin node, and the
// operator-chosen agent name only — so they are safe to propagate across nodes.
type Event struct {
	// Type is the event type: one of the EventAgent* constants.
	Type string `json:"type"`
	// Node is the id of the node the event originated on.
	Node string `json:"node"`
	// AgentID is the id of the agent the event concerns.
	AgentID string `json:"agent_id,omitempty"`
	// Name is the agent's name/type; set on spawn, omitted otherwise.
	Name string `json:"name,omitempty"`
}

// Agent lifecycle event types published on the EventBus.
const (
	// EventAgentSpawned is published after an agent subprocess starts and its
	// execution context is initialized.
	EventAgentSpawned = "agent.spawned"
	// EventAgentExiting is published when an agent is signaled to stop, before
	// the subprocess has exited.
	EventAgentExiting = "agent.exiting"
	// EventAgentExited is published after an agent subprocess has exited and
	// its proc entry has been reaped.
	EventAgentExited = "agent.exited"
)

// EventBus is an in-process, brokerless event bus backed by Go channels. Every
// subscriber receives every published event; a subscriber whose buffer is full
// is skipped (non-blocking) so a slow consumer cannot stall a publisher.
type EventBus struct {
	mu sync.Mutex

	nextSub int
	subs    map[int]chan Event
}

// NewEventBus constructs an empty EventBus.
func NewEventBus() *EventBus {
	return &EventBus{subs: make(map[int]chan Event)}
}

// Publish delivers ev to every subscriber. A subscriber whose channel is full
// is skipped (non-blocking) so a slow consumer cannot stall a publisher. The
// fan-out holds the bus lock so it never races a concurrent Subscribe/cancel
// (a cancel closes its channel under the same lock).
func (b *EventBus) Publish(ev Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, ch := range b.subs {
		select {
		case ch <- ev:
		default:
			// drop on slow subscriber
		}
	}
}

// Subscribe returns a receive-only channel of all events and a cancel func the
// caller must invoke when done. cancel unsubscribes and closes the channel.
func (b *EventBus) Subscribe() (events <-chan Event, cancel func()) {
	b.mu.Lock()
	id := b.nextSub
	b.nextSub++
	ch := make(chan Event, eventBusBufferSize)
	b.subs[id] = ch
	b.mu.Unlock()

	cancel = func() {
		b.mu.Lock()
		if _, ok := b.subs[id]; ok {
			delete(b.subs, id)
			close(ch)
		}
		b.mu.Unlock()
	}
	return ch, cancel
}

// eventBusBufferSize is the per-subscriber channel capacity. Events beyond
// this are dropped (non-blocking) so a slow subscriber cannot stall a
// publisher.
const eventBusBufferSize = 32
