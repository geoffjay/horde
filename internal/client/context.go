package client

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// ActivityState is the busy/idle runtime state of an agent.
type ActivityState string

const (
	StateBusy ActivityState = "busy"
	StateIdle ActivityState = "idle"
)

// AgentState is the lifecycle state of an agent process.
type AgentState string

const (
	AgentRunning AgentState = "running"
	AgentExiting AgentState = "exiting"
	AgentExited  AgentState = "exited"
)

// ErrorSummary is a bounded, non-sensitive projection of an agent error.
type ErrorSummary struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Fatal   bool   `json:"fatal"`
}

// ApprovalRef is a bounded reference to a pending tool-use approval.
type ApprovalRef struct {
	RequestID string `json:"request_id"`
	ToolName  string `json:"tool_name"`
}

// ExecutionContext is the materialized work-state of one agent, as returned
// by GET /agents/context and GET /agents/{id}/context.
type ExecutionContext struct {
	AgentID string `json:"agent_id"`
	NodeID  string `json:"node_id"`

	Project string `json:"project,omitempty"`
	Issue   string `json:"issue,omitempty"`

	Activity      ActivityState `json:"activity"`
	WaitingModel  bool          `json:"waiting_model"`
	Blocked       bool          `json:"blocked"`
	BlockedReason string        `json:"blocked_reason,omitempty"`
	Note          string        `json:"note,omitempty"`

	Errors           []ErrorSummary `json:"errors,omitempty"`
	PendingApprovals []ApprovalRef  `json:"pending_approvals,omitempty"`

	ErrorCount           int `json:"error_count,omitempty"`
	PendingApprovalCount int `json:"pending_approval_count,omitempty"`

	Lifecycle AgentState `json:"lifecycle"`
	TurnID    string     `json:"turn_id,omitempty"`
	UpdatedAt time.Time  `json:"updated_at"`
}

// ListAgentContexts fetches the execution contexts of all local agents.
func (c *Client) ListAgentContexts(ctx context.Context) ([]ExecutionContext, error) {
	var ctxs []ExecutionContext
	if err := c.getJSON(ctx, "/api/v1/agents/context", &ctxs); err != nil {
		return nil, err
	}
	return ctxs, nil
}

// GetAgentContext fetches the execution context snapshot for one agent.
func (c *Client) GetAgentContext(ctx context.Context, id string) (ExecutionContext, error) {
	var ec ExecutionContext
	if err := c.getJSON(ctx, "/api/v1/agents/"+id+"/context", &ec); err != nil {
		return ec, err
	}
	return ec, nil
}

// StreamAgentContext subscribes to the SSE context stream for one agent.
// It returns a channel that receives each ExecutionContext snapshot (the
// first immediately, then deltas) and an error if the initial request fails.
// The stream runs until ctx is canceled; the channel is closed when the
// stream ends. A non-nil err is returned only if the connection could not be
// established — once streaming, decode or transport errors terminate the
// stream by closing the channel without error.
func (c *Client) StreamAgentContext(ctx context.Context, id string) (<-chan ExecutionContext, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.baseURL+"/api/v1/agents/"+id+"/context/stream", http.NoBody)
	if err != nil {
		return nil, err
	}
	resp, err := c.streamHTTP.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("stream context: %s", resp.Status)
	}

	ch := make(chan ExecutionContext, 1)
	go streamContextEvents(ctx, resp, ch)
	return ch, nil
}

// streamContextEvents reads SSE context events from resp.Body, parses each
// data payload into an ExecutionContext, and sends it on ch. It closes
// resp.Body and ch when done (on ctx cancel or end of stream).
func streamContextEvents(ctx context.Context, resp *http.Response, ch chan<- ExecutionContext) {
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
		var ec ExecutionContext
		if json.Unmarshal([]byte(data), &ec) != nil {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case ch <- ec:
		}
	}
}
