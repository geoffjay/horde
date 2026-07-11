package agentapi_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/session"

	"github.com/geoffjay/horde/agents"
	"github.com/geoffjay/horde/internal/agentapi"
)

// newTestHandler creates an agentapi.Handler backed by a real runner over
// the greeter agent and an in-memory session service.
func newTestHandler(t *testing.T) *agentapi.Handler {
	t.Helper()
	a, err := agents.Get("greeter")
	require.NoError(t, err)
	r, err := runner.New(runner.Config{
		AppName:           "horde-test",
		Agent:             a,
		SessionService:    session.InMemoryService(),
		AutoCreateSession: true,
	})
	require.NoError(t, err)
	return agentapi.NewHandler(r, "greeter")
}

func TestGetHealth(t *testing.T) {
	h := newTestHandler(t)
	ts := httptest.NewServer(h.Router())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/health")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var hr struct {
		Status string `json:"status"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&hr))
	assert.Equal(t, "ok", hr.Status)
}

func TestInvoke_BadBody(t *testing.T) {
	h := newTestHandler(t)
	ts := httptest.NewServer(h.Router())
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/invoke", "application/json",
		strings.NewReader("not json"))
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestInvoke_StreamsEventsAndDone(t *testing.T) {
	h := newTestHandler(t)
	ts := httptest.NewServer(h.Router())
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/invoke", "application/json",
		strings.NewReader(`{"message":"hello"}`))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	sawInvocation, sawToken, sawDone := false, false, false
	for _, line := range strings.Split(string(body), "\n") {
		switch {
		case strings.HasPrefix(line, "event: invocation"):
			sawInvocation = true
		case strings.HasPrefix(line, "event: token"):
			sawToken = true
		case strings.HasPrefix(line, "event: done"):
			sawDone = true
		}
	}
	assert.True(t, sawInvocation, "expected invocation event")
	assert.True(t, sawToken, "expected token event")
	assert.True(t, sawDone, "expected done event")
}

func TestInvoke_EventIDsAreSequential(t *testing.T) {
	h := newTestHandler(t)
	ts := httptest.NewServer(h.Router())
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/invoke", "application/json",
		strings.NewReader(`{"message":"hello"}`))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	var ids []int
	for _, line := range strings.Split(string(body), "\n") {
		if strings.HasPrefix(line, "id: ") {
			n, err := strconv.Atoi(strings.TrimPrefix(line, "id: "))
			require.NoError(t, err)
			ids = append(ids, n)
		}
	}
	require.NotEmpty(t, ids)
	for i := 1; i < len(ids); i++ {
		assert.Equal(t, ids[i-1]+1, ids[i], "ids should be sequential")
	}
}

func TestInvoke_ResumeReplaysFromBuffer(t *testing.T) {
	h := newTestHandler(t)
	ts := httptest.NewServer(h.Router())
	defer ts.Close()

	// First request: run the invocation to completion.
	resp1, err := http.Post(ts.URL+"/invoke", "application/json",
		strings.NewReader(`{"message":"hello"}`))
	require.NoError(t, err)
	body1Bytes, err := io.ReadAll(resp1.Body)
	require.NoError(t, err)
	resp1.Body.Close()
	body1 := string(body1Bytes)

	invID := extractField(t, body1, "invocation_id")
	require.NotEmpty(t, invID)
	firstIDs := extractIDs(body1)
	require.NotEmpty(t, firstIDs)

	// Second request: reconnect with the same invocation_id and
	// Last-Event-ID: 0 — should replay all buffered events.
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/invoke",
		strings.NewReader(`{"message":"hello","invocation_id":"`+invID+`"}`))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Last-Event-ID", "0")

	resp2, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp2.Body.Close()
	body2Bytes, err := io.ReadAll(resp2.Body)
	require.NoError(t, err)
	body2 := string(body2Bytes)

	replayedIDs := extractIDs(body2)
	assert.NotEmpty(t, replayedIDs)
	assert.Equal(t, firstIDs, replayedIDs,
		"reconnect with Last-Event-ID:0 should replay the same events")
}

func TestInvoke_RunnerRunEnteredOnce(t *testing.T) {
	h := newTestHandler(t)
	ts := httptest.NewServer(h.Router())
	defer ts.Close()

	// Run an invocation.
	resp1, err := http.Post(ts.URL+"/invoke", "application/json",
		strings.NewReader(`{"message":"hello"}`))
	require.NoError(t, err)
	body1Bytes, err := io.ReadAll(resp1.Body)
	require.NoError(t, err)
	resp1.Body.Close()
	body1 := string(body1Bytes)

	invID := extractField(t, body1, "invocation_id")
	require.NotEmpty(t, invID)
	ids1 := extractIDs(body1)

	// Reconnect with the same invocation_id.
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/invoke",
		strings.NewReader(`{"message":"hello","invocation_id":"`+invID+`"}`))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Last-Event-ID", "0")

	resp2, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp2.Body.Close()
	body2Bytes, err := io.ReadAll(resp2.Body)
	require.NoError(t, err)
	body2 := string(body2Bytes)
	ids2 := extractIDs(body2)

	// No new run: ids match exactly (replay only, no new events).
	assert.Equal(t, ids1, ids2,
		"reconnect must not produce new events (no re-run)")
}

func TestInvoke_DisconnectDoesNotCancelRun(t *testing.T) {
	h := newTestHandler(t)
	ts := httptest.NewServer(h.Router())
	defer ts.Close()

	// Start an invocation, then cancel the client almost immediately.
	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		ts.URL+"/invoke", strings.NewReader(`{"message":"hello"}`))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")

	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	resp, err := http.DefaultClient.Do(req)
	if err == nil {
		// Read what we can before the body is closed.
		bodyBytes, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		body := string(bodyBytes)
		invID := extractField(t, body, "invocation_id")
		if invID == "" {
			t.Skip("client cancelled before invocation_id was available")
		}

		// Wait for the background run to complete.
		time.Sleep(100 * time.Millisecond)

		// Reconnect and verify all events (including done) are buffered.
		req2, err := http.NewRequest(http.MethodPost, ts.URL+"/invoke",
			strings.NewReader(`{"message":"hello","invocation_id":"`+invID+`"}`))
		require.NoError(t, err)
		req2.Header.Set("Content-Type", "application/json")
		req2.Header.Set("Last-Event-ID", "0")

		resp2, err := http.DefaultClient.Do(req2)
		require.NoError(t, err)
		defer resp2.Body.Close()
		body2Bytes, err := io.ReadAll(resp2.Body)
		require.NoError(t, err)
		body2 := string(body2Bytes)

		assert.NotEmpty(t, extractIDs(body2),
			"reconnect should find buffered events")
		assert.Contains(t, body2, "event: done",
			"reconnect should find the done event from the background run")
	}
}

// extractField finds the first data: line after an "event: <eventType>" line
// and extracts a field from the JSON object.
func extractField(t *testing.T, body, field string) string {
	t.Helper()
	lines := strings.Split(body, "\n")
	for i, line := range lines {
		if strings.HasPrefix(line, "event: invocation") {
			if i+1 < len(lines) && strings.HasPrefix(lines[i+1], "data: ") {
				data := strings.TrimPrefix(lines[i+1], "data: ")
				var m map[string]string
				if err := json.Unmarshal([]byte(data), &m); err != nil {
					return ""
				}
				return m[field]
			}
		}
	}
	return ""
}

func extractIDs(body string) []int {
	var ids []int
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "id: ") {
			n, err := strconv.Atoi(strings.TrimPrefix(line, "id: "))
			if err == nil {
				ids = append(ids, n)
			}
		}
	}
	return ids
}
