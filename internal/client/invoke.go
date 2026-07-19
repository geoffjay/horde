package client

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// InvokeRequest is the body for POST /api/v1/agents/{id}/invoke.
type InvokeRequest struct {
	Message      string `json:"message"`
	InvocationID string `json:"invocation_id,omitempty"`
}

// InvokeEvent is one parsed SSE event from the invoke stream. Type is the
// SSE event name (invocation, token, error, done) and Data is the raw JSON
// payload. Call Decode on a typed struct to unmarshal Data.
type InvokeEvent struct {
	Type string
	Data json.RawMessage
}

// InvokeResult is the JSON payload of the invocation event — the first event
// the agent emits, carrying the invocation id.
type InvokeResult struct {
	InvocationID string `json:"invocation_id"`
}

// InvokeToken is the JSON payload of a token event — one chunk of the
// agent's streamed response.
type InvokeToken struct {
	Text string `json:"text"`
}

// InvokeError is the JSON payload of an error event.
type InvokeError struct {
	InvocationID string `json:"invocation_id"`
	Code         string `json:"code"`
	Message      string `json:"message"`
}

// InvokeDone is the JSON payload of the done event marking the end of a run.
type InvokeDone struct {
	InvocationID string `json:"invocation_id"`
}

// sseScannerBufferSize is the initial buffer size for the SSE scanner.
const sseScannerInit = 64 * 1024

// sseScannerMax is the maximum line size the SSE scanner accepts.
const sseScannerMax = 1024 * 1024

// invokeChanSize is the buffer size for the invoke event channel.
const invokeChanSize = 8

// Invoke streams the SSE response from POST /api/v1/agents/{id}/invoke. It
// returns a channel of parsed events and an error if the initial request
// fails (including a 409 when the project is paused). The stream runs until
// ctx is canceled or the agent emits the done event; in either case the
// channel is closed.
func (c *Client) Invoke(ctx context.Context, id string, req InvokeRequest) (<-chan InvokeEvent, error) {
	body, _ := json.Marshal(req)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.currentBase()+"/api/v1/agents/"+id+"/invoke", strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := c.streamHTTP.Do(httpReq)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("invoke: %s", resp.Status)
	}
	ch := make(chan InvokeEvent, invokeChanSize)
	go streamInvokeEvents(ctx, resp, ch)
	return ch, nil
}

// streamInvokeEvents reads SSE events from resp.Body, parses them into
// InvokeEvent values, and sends them on ch. It closes resp.Body and ch when
// done (on ctx cancel, the done event, or end of stream).
func streamInvokeEvents(ctx context.Context, resp *http.Response, ch chan<- InvokeEvent) {
	defer resp.Body.Close()
	defer close(ch)
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, sseScannerInit), sseScannerMax)
	var evType string
	var dataLines []string
	flush := func() (done bool) {
		if evType == "" || len(dataLines) == 0 {
			evType, dataLines = "", nil
			return false
		}
		joined := strings.Join(dataLines, "\n")
		typ := evType
		dataLines = nil
		evType = ""
		ev := InvokeEvent{Type: typ, Data: json.RawMessage(joined)}
		select {
		case <-ctx.Done():
			return true
		case ch <- ev:
		}
		return typ == "done"
	}
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case line == "":
			if flush() {
				return
			}
		case strings.HasPrefix(line, "event: "):
			evType = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			dataLines = append(dataLines, strings.TrimPrefix(line, "data: "))
		default:
			// Other SSE fields (id:, comments) are not used by the client.
		}
	}
	flush()
}
