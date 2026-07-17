package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/geoffjay/horde/internal/server"
)

// fakeEventView is a test double for the event stream handlers.
type fakeEventView struct {
	mode      server.Mode
	events    chan server.Event
	published []server.Event
}

func (f *fakeEventView) Mode() server.Mode { return f.mode }

func (f *fakeEventView) SubscribeEvents() (<-chan server.Event, func()) {
	return f.events, func() {}
}

func (f *fakeEventView) PublishClusterEvent(ev server.Event) {
	f.published = append(f.published, ev)
}

func TestStreamEvents_WritesSSEFrames(t *testing.T) {
	// Unbuffered channel: a send returns only once the handler has received,
	// so the second send guarantees the first frame is fully written.
	ch := make(chan server.Event)
	fake := &fakeEventView{mode: server.ModeMaster, events: ch}

	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, "/api/v1/events/stream", nil).WithContext(ctx)
	rec := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		streamEvents(fake)(rec, req)
		close(done)
	}()

	ch <- server.Event{Type: server.EventAgentSpawned, Node: "master-1", AgentID: "a1-7", Name: "greeter"}
	ch <- server.Event{Type: server.EventAgentExited, Node: "master-1", AgentID: "a1-7"}
	cancel()
	<-done

	assert.Equal(t, "text/event-stream", rec.Header().Get("Content-Type"))
	body := rec.Body.String()
	assert.Contains(t, body, "id: 1")
	assert.Contains(t, body, "event: agent.spawned")
	assert.Contains(t, body, `"agent_id":"a1-7"`)
	assert.Contains(t, body, `"name":"greeter"`)
}

func TestReceiveClusterEvent_MasterRepublishes(t *testing.T) {
	fake := &fakeEventView{mode: server.ModeMaster}
	body, err := json.Marshal(server.Event{Type: server.EventAgentSpawned, Node: "slave-1", AgentID: "a1"})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/cluster/events", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	receiveClusterEvent(fake)(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Len(t, fake.published, 1)
	assert.Equal(t, "slave-1", fake.published[0].Node)
}

func TestReceiveClusterEvent_SlaveRejects(t *testing.T) {
	fake := &fakeEventView{mode: server.ModeSlave}
	body, err := json.Marshal(server.Event{Type: server.EventAgentSpawned})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/cluster/events", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	receiveClusterEvent(fake)(rec, req)

	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Empty(t, fake.published)
}

func TestReceiveClusterEvent_RejectsBadBodyAndMissingType(t *testing.T) {
	fake := &fakeEventView{mode: server.ModeMaster}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/cluster/events", bytes.NewReader([]byte("not json")))
	rec := httptest.NewRecorder()
	receiveClusterEvent(fake)(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)

	body, err := json.Marshal(server.Event{Node: "slave-1"}) // no Type
	require.NoError(t, err)
	req = httptest.NewRequest(http.MethodPost, "/api/v1/cluster/events", bytes.NewReader(body))
	rec = httptest.NewRecorder()
	receiveClusterEvent(fake)(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)

	assert.Empty(t, fake.published)
}

// TestClusterEvents_RepublishedThroughRouter exercises the full wiring: a POST
// to /cluster/events on the master is republished onto the bus and reaches a
// local /events/stream subscriber.
func TestClusterEvents_RepublishedThroughRouter(t *testing.T) {
	srv := newTestServer(t)
	h := Router(srv)

	ch, cancel := srv.SubscribeEvents()
	defer cancel()

	w := do(t, h, http.MethodPost, "/api/v1/cluster/events", server.Event{
		Type: server.EventAgentSpawned, Node: "slave-1", AgentID: "a1-9", Name: "greeter",
	})
	require.Equal(t, http.StatusOK, w.Code)

	select {
	case ev := <-ch:
		assert.Equal(t, "slave-1", ev.Node)
		assert.Equal(t, "a1-9", ev.AgentID)
	case <-time.After(time.Second):
		t.Fatal("event not republished onto bus")
	}
}
