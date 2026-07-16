package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/geoffjay/horde/internal/client"
)

// invokeTestHandler returns an http.Handler that serves a minimal node API
// with an invoke SSE endpoint that streams a fixed conversation.
func invokeTestHandler(tokens []string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/health", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})
	mux.HandleFunc("/api/v1/node", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"mode": "master", "leader_connected": true, "node_id": "n1", "version": "test",
		})
	})
	mux.HandleFunc("/api/v1/agents", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode([]client.Agent{{ID: "a1", Name: "greeter", Status: "running"}})
	})
	mux.HandleFunc("/api/v1/agents/context", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode([]client.ExecutionContext{{AgentID: "a1", Activity: client.StateIdle}})
	})
	mux.HandleFunc("/api/v1/projects/", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode([]client.Project{
			{ID: "p1", Name: "auth", State: "active", Team: client.ProjectTeam{Agents: []client.TeamAgent{
				{AgentID: "a1", Name: "greeter"},
			}}},
		})
	})
	mux.HandleFunc("/api/v1/agents/a1/invoke", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		writeSSEEvent(w, flusher, "invocation", map[string]string{"invocation_id": "inv-1"})
		for _, tok := range tokens {
			writeSSEEvent(w, flusher, "token", map[string]string{"text": tok})
		}
		writeSSEEvent(w, flusher, "done", map[string]string{"invocation_id": "inv-1"})
	})
	return mux
}

func writeSSEEvent(w http.ResponseWriter, flusher http.Flusher, eventType string, v any) {
	data, _ := json.Marshal(v)
	_, _ = w.Write([]byte("event: " + eventType + "\n"))
	_, _ = w.Write([]byte("data: "))
	_, _ = w.Write(data)
	_, _ = w.Write([]byte("\n\n"))
	if flusher != nil {
		flusher.Flush()
	}
}

func TestSendInvoke_AppendsUserMessageAndStreams(t *testing.T) {
	srv := httptest.NewServer(invokeTestHandler([]string{"hello", " world"}))
	defer srv.Close()

	m := New(context.Background(), srv.Listener.Addr().String())
	m.Update(m.connect())
	m.Update(m.loadNode())
	m.connected = true

	// Drill into project → agent → invoke.
	m.Update(namedKey(tea.KeyEnter)) // projects → projectDetail
	m.Update(namedKey(tea.KeyEnter)) // projectDetail → agent
	m.Update(namedKey(tea.KeyEnter)) // agent → invoke
	require.Equal(t, viewInvoke, m.view)

	// Type a message and send it.
	m.invokeInput = "hi there"
	_, cmd := m.handleInvokeKey(keyPress("enter"))
	require.NotNil(t, cmd)

	// The user message should be in the transcript.
	require.Len(t, m.invokeTranscript, 1)
	assert.Equal(t, "user", m.invokeTranscript[0].role)
	assert.Equal(t, "hi there", m.invokeTranscript[0].text)
	assert.True(t, m.invokeStreaming)
	assert.Empty(t, m.invokeInput, "input should be cleared after send")

	// The command opens the stream (in a goroutine); it returns an
	// invokeStartedMsg. Feeding that to Update begins pumping events.
	started, ok := cmd().(invokeStartedMsg)
	require.True(t, ok)
	require.NoError(t, started.err)
	_, pumpCmd := m.Update(started)
	require.NotNil(t, pumpCmd, "handleInvokeStarted should return a pump command")

	// Pump the first event (invocation) — no transcript change.
	ev, ok := pumpCmd().(invokeEventMsg)
	require.True(t, ok)
	assert.Equal(t, "invocation", ev.ev.Type)

	m.Update(ev)
	// Pump the next event (token "hello").
	msg2 := m.pumpInvoke()()
	ev2, ok := msg2.(invokeEventMsg)
	require.True(t, ok)
	assert.Equal(t, "token", ev2.ev.Type)

	m.Update(ev2)
	require.Len(t, m.invokeTranscript, 2)
	assert.Equal(t, "agent", m.invokeTranscript[1].role)
	assert.Equal(t, "hello", m.invokeTranscript[1].text)

	// Pump the next event (token " world").
	msg3 := m.pumpInvoke()()
	ev3, ok := msg3.(invokeEventMsg)
	require.True(t, ok)
	m.Update(ev3)
	assert.Equal(t, "hello world", m.invokeTranscript[1].text, "second token should append to agent entry")
}

func TestSendInvoke_409SetsError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/health", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})
	mux.HandleFunc("/api/v1/node", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"mode": "master", "leader_connected": true, "node_id": "n1"})
	})
	mux.HandleFunc("/api/v1/agents", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode([]client.Agent{{ID: "a1", Name: "greeter"}})
	})
	mux.HandleFunc("/api/v1/agents/context", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode([]any{})
	})
	mux.HandleFunc("/api/v1/projects/", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode([]client.Project{
			{ID: "p1", Name: "auth", State: "paused", Team: client.ProjectTeam{Agents: []client.TeamAgent{
				{AgentID: "a1", Name: "greeter"},
			}}},
		})
	})
	mux.HandleFunc("/api/v1/agents/a1/invoke", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "project is paused"})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	m := New(context.Background(), srv.Listener.Addr().String())
	m.Update(m.connect())
	m.Update(m.loadNode())
	m.connected = true

	m.Update(namedKey(tea.KeyEnter))
	m.Update(namedKey(tea.KeyEnter))
	m.Update(namedKey(tea.KeyEnter))
	require.Equal(t, viewInvoke, m.view)

	m.invokeInput = "hi"
	cmd := m.sendInvoke("a1")
	require.NotNil(t, cmd, "sendInvoke should return a command that opens the stream")
	// The stream is opened in the command goroutine; run it and feed the
	// resulting invokeStartedMsg back through Update to surface the error.
	m.Update(cmd())

	assert.False(t, m.invokeStreaming, "should not be streaming on error")
	assert.Contains(t, m.invokeErr, "409")
}

func TestInvokeKey_TypesIntoInput(t *testing.T) {
	m := New(context.Background(), "127.0.0.1:1")
	m.connected = true
	m.view = viewInvoke

	m.handleInvokeKey(keyPress("h"))
	m.handleInvokeKey(keyPress("i"))
	assert.Equal(t, "hi", m.invokeInput)
}

func TestInvokeKey_BackspaceDeletes(t *testing.T) {
	m := New(context.Background(), "127.0.0.1:1")
	m.connected = true
	m.view = viewInvoke
	m.invokeInput = "hello"

	m.handleInvokeKey(namedKey(tea.KeyBackspace))
	assert.Equal(t, "hell", m.invokeInput)
}

func TestInvokeKey_EnterDoesNothingWhenEmpty(t *testing.T) {
	m := New(context.Background(), "127.0.0.1:1")
	m.connected = true
	m.view = viewInvoke
	m.invokeInput = ""

	_, cmd := m.handleInvokeKey(keyPress("enter"))
	assert.Nil(t, cmd, "enter with empty input should not send")
	assert.Empty(t, m.invokeTranscript)
}

func TestInvokeKey_EnterDoesNothingWhenStreaming(t *testing.T) {
	m := New(context.Background(), "127.0.0.1:1")
	m.connected = true
	m.view = viewInvoke
	m.invokeInput = "hello"
	m.invokeStreaming = true

	_, cmd := m.handleInvokeKey(keyPress("enter"))
	assert.Nil(t, cmd, "enter while streaming should not send")
}

func TestInvokeKey_EscPopsBack(t *testing.T) {
	m := New(context.Background(), "127.0.0.1:1")
	m.connected = true
	m.view = viewInvoke
	m.crumbs = []breadcrumbEntry{{view: viewAgent, label: "agent"}}

	m.handleInvokeKey(escKey())
	assert.Equal(t, viewAgent, m.view)
}

func TestUnsubscribeInvokeOnPopView(t *testing.T) {
	m := New(context.Background(), "127.0.0.1:1")
	m.connected = true
	m.view = viewInvoke
	m.invokeStreaming = true
	m.invokeCancel = func() {}

	m.crumbs = []breadcrumbEntry{{view: viewAgent}}
	m.popView()
	assert.False(t, m.invokeStreaming)
	assert.Nil(t, m.invokeCancel)
}

func TestResetInvokeState(t *testing.T) {
	m := New(context.Background(), "127.0.0.1:1")
	m.invokeTranscript = []transcriptEntry{{role: "user", text: "hi"}}
	m.invokeInput = "typing"
	m.invokeStreaming = true
	m.invokeErr = "some error"

	m.resetInvokeState()
	assert.Nil(t, m.invokeTranscript)
	assert.Empty(t, m.invokeInput)
	assert.False(t, m.invokeStreaming)
	assert.Empty(t, m.invokeErr)
}

func TestRenderInvokeView_SessionBanner(t *testing.T) {
	m := New(context.Background(), "127.0.0.1:1")
	m.connected = true
	m.view = viewInvoke
	m.selectedProjectID = "p1"
	m.cursor = 0
	m.projects = []client.Project{{ID: "p1", Name: "auth", State: "active", Team: client.ProjectTeam{Agents: []client.TeamAgent{
		{AgentID: "a1", Name: "coder"},
	}}}}

	out := m.renderInvokeView()
	assert.Contains(t, out, "session coder:p1")
	assert.Contains(t, out, "multi-turn")
}

func TestRenderInvokeView_Transcript(t *testing.T) {
	m := New(context.Background(), "127.0.0.1:1")
	m.connected = true
	m.view = viewInvoke
	m.selectedProjectID = "p1"
	m.cursor = 0
	m.projects = []client.Project{{ID: "p1", Name: "auth", State: "active", Team: client.ProjectTeam{Agents: []client.TeamAgent{
		{AgentID: "a1", Name: "coder"},
	}}}}
	m.invokeTranscript = []transcriptEntry{
		{role: "user", text: "add a rate limiter"},
		{role: "agent", text: "I'll add a token-bucket limiter."},
	}

	out := m.renderInvokeView()
	assert.Contains(t, out, "› you")
	assert.Contains(t, out, "add a rate limiter")
	assert.Contains(t, out, "coder")
	assert.Contains(t, out, "token-bucket limiter")
}

func TestRenderInvokeView_ErrorNotice(t *testing.T) {
	m := New(context.Background(), "127.0.0.1:1")
	m.connected = true
	m.view = viewInvoke
	m.selectedProjectID = "p1"
	m.cursor = 0
	m.projects = []client.Project{{ID: "p1", Name: "auth", State: "active", Team: client.ProjectTeam{Agents: []client.TeamAgent{
		{AgentID: "a1", Name: "coder"},
	}}}}
	m.invokeErr = "invoke: 409 Conflict"

	out := m.renderInvokeView()
	assert.Contains(t, out, "409 Conflict")
}

func TestRenderInvokeView_StreamingIndicator(t *testing.T) {
	m := New(context.Background(), "127.0.0.1:1")
	m.connected = true
	m.view = viewInvoke
	m.selectedProjectID = "p1"
	m.cursor = 0
	m.projects = []client.Project{{ID: "p1", Name: "auth", State: "active", Team: client.ProjectTeam{Agents: []client.TeamAgent{
		{AgentID: "a1", Name: "coder"},
	}}}}
	m.invokeStreaming = true

	out := m.renderInvokeView()
	assert.Contains(t, out, "streaming")
}

func TestRenderInvokeView_NoAgentSelected(t *testing.T) {
	m := New(context.Background(), "127.0.0.1:1")
	m.connected = true
	m.view = viewInvoke
	m.projects = nil

	out := m.renderInvokeView()
	assert.Contains(t, out, "no agent selected")
}
