package server

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEventBus_PublishReachesSubscriber(t *testing.T) {
	bus := NewEventBus()
	ch, cancel := bus.Subscribe()
	defer cancel()

	bus.Publish(Event{Type: EventAgentSpawned, Node: "master-1", AgentID: "a1", Name: "greeter"})

	ev := <-ch
	assert.Equal(t, EventAgentSpawned, ev.Type)
	assert.Equal(t, "master-1", ev.Node)
	assert.Equal(t, "a1", ev.AgentID)
	assert.Equal(t, "greeter", ev.Name)
}

func TestEventBus_FansOutToEverySubscriber(t *testing.T) {
	bus := NewEventBus()
	ch1, cancel1 := bus.Subscribe()
	defer cancel1()
	ch2, cancel2 := bus.Subscribe()
	defer cancel2()

	bus.Publish(Event{Type: EventAgentExited, Node: "n", AgentID: "a2"})

	assert.Equal(t, "a2", (<-ch1).AgentID)
	assert.Equal(t, "a2", (<-ch2).AgentID)
}

func TestEventBus_DropsWhenSubscriberBufferFull(t *testing.T) {
	bus := NewEventBus()
	_, cancel := bus.Subscribe() // never drained
	defer cancel()

	// Publishing far more than the buffer must not block or panic; excess
	// events are dropped for the slow subscriber.
	for i := 0; i < eventBusBufferSize*4; i++ {
		bus.Publish(Event{Type: EventAgentSpawned, Node: "n", AgentID: "a"})
	}
}

func TestEventBus_CancelStopsDelivery(t *testing.T) {
	bus := NewEventBus()
	ch, cancel := bus.Subscribe()

	cancel()
	// The channel is closed by cancel; a receive returns the zero value with
	// ok=false rather than a published event.
	_, ok := <-ch
	assert.False(t, ok, "channel is closed after cancel")

	// Publishing after cancel is a no-op (the subscription is gone) and must
	// not panic on the closed channel.
	bus.Publish(Event{Type: EventAgentSpawned, Node: "n", AgentID: "a"})
}

func TestServer_SubscribeEventsReceivesClusterEvent(t *testing.T) {
	srv, err := New(Config{Mode: ModeMaster, NodeID: "master-1", SpawnDefaultAgent: false})
	require.NoError(t, err)

	ch, cancel := srv.SubscribeEvents()
	defer cancel()

	// A slave-forwarded event, republished onto the master bus, reaches
	// local subscribers with its origin node preserved.
	srv.PublishClusterEvent(Event{Type: EventAgentSpawned, Node: "slave-1", AgentID: "a7-42", Name: "greeter"})

	ev := <-ch
	assert.Equal(t, EventAgentSpawned, ev.Type)
	assert.Equal(t, "slave-1", ev.Node, "origin node is preserved across the hop")
	assert.Equal(t, "a7-42", ev.AgentID)
}
