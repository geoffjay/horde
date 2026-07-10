package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

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

// ctrlKey constructs a KeyPressMsg for ctrl+<c> (e.g. ctrlKey('p') == ctrl+p).
func ctrlKey(c rune) tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: c, Mod: tea.ModCtrl}
}

// escKey constructs a KeyPressMsg for the escape key.
func escKey() tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: tea.KeyEscape}
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

func TestModel_PaletteToggle(t *testing.T) {
	m := New(context.Background(), "127.0.0.1:1")
	require.False(t, m.paletteOpen)

	// ctrl+p opens, then closes.
	m.Update(ctrlKey('p'))
	assert.True(t, m.paletteOpen)
	m.Update(ctrlKey('p'))
	assert.False(t, m.paletteOpen)

	// esc always closes.
	m.Update(ctrlKey('p'))
	require.True(t, m.paletteOpen)
	m.Update(escKey())
	assert.False(t, m.paletteOpen)

	// A command key ("r") also dismisses the palette.
	m.Update(ctrlKey('p'))
	require.True(t, m.paletteOpen)
	m.Update(keyPress("r"))
	assert.False(t, m.paletteOpen)
}

func TestStatusLine_RightAlignedBlocks(t *testing.T) {
	m := New(context.Background(), "127.0.0.1:1")
	m.connected = true

	const width = 80
	out := m.status.Render(m, width)

	// Both blocks and the default separator are present, and the whole line
	// is padded to the full width (right-aligned).
	assert.Contains(t, out, "connected")
	assert.Contains(t, out, "ctrl+p")
	assert.Contains(t, out, "commands")
	assert.Contains(t, out, defaultSeparator)
	assert.Equal(t, width, lipgloss.Width(out))
	assert.True(t, strings.HasPrefix(out, " "), "expected left padding for right alignment")
}

func TestStatusLine_AddRemove(t *testing.T) {
	s := NewStatusLine()
	s.Add(StatusBlock{Name: "a", Render: func(*Model) string { return "A" }})
	s.Add(StatusBlock{Name: "b", Render: func(*Model) string { return "B" }})

	m := New(context.Background(), "127.0.0.1:1")
	assert.Contains(t, s.Render(m, 0), "A")
	assert.Contains(t, s.Render(m, 0), "B")

	assert.True(t, s.Remove("a"))
	assert.False(t, s.Remove("a"), "removing a missing block returns false")
	assert.NotContains(t, s.Render(m, 0), "A")
	assert.Contains(t, s.Render(m, 0), "B")
}

func TestModel_ViewOverlaysPaletteWhenOpen(t *testing.T) {
	m := New(context.Background(), "127.0.0.1:1")
	m.connected = true
	m.width, m.height = 80, 24

	assert.NotContains(t, m.View().Content, "Commands")

	m.paletteOpen = true
	content := m.View().Content
	assert.Contains(t, content, "Commands")
	assert.Contains(t, content, "Refresh")
	assert.Contains(t, content, "Quit")
}
