package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestHandler returns an http.Handler serving minimal valid responses
// for the node API endpoints the TUI client calls.
func newTestHandler() http.Handler {
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
		_ = json.NewEncoder(w).Encode([]any{})
	})
	return mux
}

// keyPress constructs a KeyPressMsg for a single printable character.
func keyPress(s string) tea.KeyPressMsg {
	return tea.KeyPressMsg{Text: s, Code: rune(s[0])}
}

func TestModel_ConnectsToReachableNode(t *testing.T) {
	stub := httptest.NewServer(newTestHandler())
	defer stub.Close()

	m := New(context.Background(), stub.Listener.Addr().String())
	msg := m.connect()

	res, ok := msg.(connectResultMsg)
	require.True(t, ok)
	assert.NoError(t, res.err)
}

func TestModel_RetryWhenNoNode(t *testing.T) {
	// Nothing listening on this port.
	m := New(context.Background(), "127.0.0.1:1")
	msg := m.connect()

	res, ok := msg.(connectResultMsg)
	require.True(t, ok)
	require.Error(t, res.err)

	// A failed connect arms the retry countdown.
	m.Update(msg)
	assert.True(t, m.retrying)
	assert.Equal(t, retryInterval, m.retryIn)
}

func TestModel_ImmediateRetryResetsTimer(t *testing.T) {
	m := New(context.Background(), "127.0.0.1:1")
	m.Update(m.connect())
	require.True(t, m.retrying)

	// Simulate partway through the countdown.
	m.retryIn = 30 * time.Second

	// Pressing "r" should trigger an immediate retry regardless of the
	// remaining countdown.
	model, cmd := m.Update(keyPress("r"))
	assert.Same(t, m, model)
	require.NotNil(t, cmd)
	assert.Equal(t, time.Duration(0), m.retryIn)
}
