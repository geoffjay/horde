package client

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// Event is one cluster-activity event from the node event stream: an agent
// lifecycle transition on some node. On the master the stream is cluster-wide.
type Event struct {
	Type    string `json:"type"`
	Node    string `json:"node"`
	AgentID string `json:"agent_id,omitempty"`
	Name    string `json:"name,omitempty"`
}

// Cluster-activity event types (mirror internal/server EventAgent* constants).
const (
	EventAgentSpawned = "agent.spawned"
	EventAgentExiting = "agent.exiting"
	EventAgentExited  = "agent.exited"
)

// StreamEvents subscribes to the SSE cluster-activity stream
// (GET /api/v1/events/stream). It returns a channel that receives each Event
// and an error if the initial request fails. The stream runs until ctx is
// canceled; the channel is closed when the stream ends. Mirrors
// StreamAgentContext: a non-nil err is returned only if the connection could
// not be established — once streaming, transport errors close the channel
// without error.
func (c *Client) StreamEvents(ctx context.Context) (<-chan Event, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.currentBase()+"/api/v1/events/stream", http.NoBody)
	if err != nil {
		return nil, err
	}
	resp, err := c.streamHTTP.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("stream events: %s", resp.Status)
	}

	ch := make(chan Event, 1)
	go streamEventEvents(ctx, resp, ch)
	return ch, nil
}

// streamEventEvents reads SSE frames from resp.Body, parses each data payload
// into an Event, and sends it on ch. It closes resp.Body and ch when done (on
// ctx cancel or end of stream).
func streamEventEvents(ctx context.Context, resp *http.Response, ch chan<- Event) {
	defer resp.Body.Close()
	defer close(ch)
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, sseScannerInit), sseScannerMax)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		var ev Event
		if json.Unmarshal([]byte(data), &ev) != nil {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case ch <- ev:
		}
	}
}
