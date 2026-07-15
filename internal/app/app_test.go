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
	mux.HandleFunc("/api/v1/projects/", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode([]any{})
	})
	mux.HandleFunc("/api/v1/agents/context", func(w http.ResponseWriter, _ *http.Request) {
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

// namedKey constructs a KeyPressMsg for a special key by its rune code
// (e.g. tea.KeyUp, tea.KeyEnter).
func namedKey(code rune) tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: code}
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

	// Pressing ctrl+r should trigger an immediate retry regardless of the
	// remaining countdown.
	model, cmd := m.Update(ctrlKey('r'))
	assert.Same(t, m, model)
	require.NotNil(t, cmd)
	assert.Equal(t, time.Duration(0), m.retryIn)
}

func TestModel_PaletteToggle(t *testing.T) {
	m := New(context.Background(), "127.0.0.1:1")
	require.False(t, m.pal.open)

	// ctrl+p opens, then closes.
	m.Update(ctrlKey('p'))
	assert.True(t, m.pal.open)
	m.Update(ctrlKey('p'))
	assert.False(t, m.pal.open)

	// esc always closes.
	m.Update(ctrlKey('p'))
	require.True(t, m.pal.open)
	m.Update(escKey())
	assert.False(t, m.pal.open)
}

func TestPalette_SearchFiltersAndTypesIntoQuery(t *testing.T) {
	m := New(context.Background(), "127.0.0.1:1")
	m.connected = true // commands: Refresh, Select Cluster, New Project, Switch Project, Quit
	m.openPalette()

	// Typing while the palette is open edits the query rather than acting as
	// a global shortcut.
	m.Update(keyPress("q"))
	assert.Equal(t, "q", m.pal.query)
	assert.False(t, m.quitting, "typing q must not quit while the palette is open")

	// The query filters the command list to matching labels ("Quit").
	cmds := m.filteredCommands()
	require.Len(t, cmds, 1)
	assert.Equal(t, "Quit", cmds[0].label)

	// Backspace clears the query and restores the full list.
	m.Update(namedKey(tea.KeyBackspace))
	assert.Equal(t, "", m.pal.query)
	assert.Len(t, m.filteredCommands(), 5)
}

func TestPalette_CursorNavigationClamps(t *testing.T) {
	m := New(context.Background(), "127.0.0.1:1")
	m.connected = true // 5 commands: Refresh, Select Cluster, New Project, Switch Project, Quit
	m.openPalette()
	require.Equal(t, 0, m.pal.cursor)

	// Up at the top stays at the top.
	m.Update(namedKey(tea.KeyUp))
	assert.Equal(t, 0, m.pal.cursor)

	// Down moves through the list and clamps at the last command.
	m.Update(namedKey(tea.KeyDown))
	assert.Equal(t, 1, m.pal.cursor)
	m.Update(namedKey(tea.KeyDown))
	assert.Equal(t, 2, m.pal.cursor)
	m.Update(namedKey(tea.KeyDown))
	assert.Equal(t, 3, m.pal.cursor)
	m.Update(namedKey(tea.KeyDown))
	assert.Equal(t, 4, m.pal.cursor)
	m.Update(namedKey(tea.KeyDown))
	assert.Equal(t, 4, m.pal.cursor)
}

func TestPalette_EnterRunsSelectedCommand(t *testing.T) {
	m := New(context.Background(), "127.0.0.1:1")
	m.connected = true
	m.openPalette()

	// Filter down to Quit and run it with enter.
	m.Update(keyPress("Q"))
	require.Equal(t, "Quit", m.filteredCommands()[0].label)

	_, cmd := m.Update(namedKey(tea.KeyEnter))
	assert.True(t, m.quitting, "enter should run the selected command (Quit)")
	assert.False(t, m.pal.open, "running a command closes the palette")
	require.NotNil(t, cmd)
}

func TestStatusLine_RightAlignedBlocks(t *testing.T) {
	m := New(context.Background(), "127.0.0.1:1")
	m.connected = true
	m.node.Mode = "master"
	m.node.NodeID = "n1"

	const width = 100
	out := m.status.Render(m, width)

	// The node block summarizes mode / id / agent count, the commands block
	// and the default separator are present, and the whole line is padded to
	// the full width (right-aligned).
	assert.Contains(t, out, "master")
	assert.Contains(t, out, "n1")
	assert.Contains(t, out, "0 agents")
	assert.Contains(t, out, "ctrl+p")
	assert.Contains(t, out, "commands")
	assert.Contains(t, out, defaultSeparator)
	assert.Equal(t, width, lipgloss.Width(out))
	assert.True(t, strings.HasPrefix(out, " "), "expected left padding for right alignment")
}

func TestAgentCountLabel(t *testing.T) {
	assert.Equal(t, "0 agents", agentCountLabel(0))
	assert.Equal(t, "1 agent", agentCountLabel(1))
	assert.Equal(t, "3 agents", agentCountLabel(3))
}

func TestPaletteWindow(t *testing.T) {
	// Fits within maxRows: show everything.
	start, end := paletteWindow(3, 0, 8)
	assert.Equal(t, 0, start)
	assert.Equal(t, 3, end)

	// Cursor near the top clamps the window to the start.
	start, end = paletteWindow(20, 1, 8)
	assert.Equal(t, 0, start)
	assert.Equal(t, 8, end)

	// Cursor near the bottom clamps the window to the end.
	start, end = paletteWindow(20, 19, 8)
	assert.Equal(t, 12, start)
	assert.Equal(t, 20, end)

	// Cursor in the middle centers the window.
	start, end = paletteWindow(20, 10, 8)
	assert.LessOrEqual(t, start, 10)
	assert.Greater(t, end, 10)
	assert.Equal(t, 8, end-start)
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

func TestView_EdgePadding(t *testing.T) {
	m := New(context.Background(), "127.0.0.1:1")
	m.connected = true
	m.width, m.height = 40, 10

	lines := strings.Split(m.View().Content, "\n")

	// The block fills the full height and no line exceeds the width.
	require.Len(t, lines, m.height)
	for _, ln := range lines {
		assert.LessOrEqual(t, lipgloss.Width(ln), m.width)
	}

	// Top is flush (title on the first row); left is inset by exactly one
	// column; the bottom row is reserved blank spacing.
	assert.True(t, strings.HasPrefix(lines[0], " ") && !strings.HasPrefix(lines[0], "  "),
		"expected 1-col left inset, got %q", lines[0])
	assert.Contains(t, lines[0], "horde")
	assert.Equal(t, "", strings.TrimSpace(lines[m.height-1]), "expected blank bottom row")
}

func TestModel_ViewOverlaysPaletteWhenOpen(t *testing.T) {
	m := New(context.Background(), "127.0.0.1:1")
	m.connected = true
	m.width, m.height = 80, 24

	assert.NotContains(t, m.View().Content, "Commands")

	m.openPalette()
	content := m.View().Content
	assert.Contains(t, content, "Commands")
	assert.Contains(t, content, "Refresh")
	assert.Contains(t, content, "Quit")
}
