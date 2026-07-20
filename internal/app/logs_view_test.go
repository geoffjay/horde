package app

import (
	"context"
	"strings"
	"testing"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGoLogs_ClearsSelectionAndEntersView(t *testing.T) {
	m := New(context.Background(), "127.0.0.1:1")
	m.selectedAgentID = "stale"
	m.selectedProjectID = "p1"
	m.logScroll = 5

	m.goLogs()

	assert.Equal(t, viewLogs, m.view)
	assert.Empty(t, m.crumbs)
	assert.Empty(t, m.selectedAgentID)
	assert.Empty(t, m.selectedProjectID)
	assert.Equal(t, 0, m.logScroll, "entering the logs page pins to the newest line")
}

func TestLogsView_EmptyBufferShowsPlaceholder(t *testing.T) {
	m := New(context.Background(), "127.0.0.1:1")
	m.view = viewLogs
	assert.Contains(t, m.renderLogsView(), "no client log output yet")
}

func TestLogsView_RendersBufferedLines(t *testing.T) {
	m := New(context.Background(), "127.0.0.1:1")
	m.view = viewLogs
	m.width, m.height = 80, 24

	_, err := m.logs.Write([]byte("hello from the client\nsecond line\n"))
	require.NoError(t, err)

	out := m.renderLogsView()
	assert.Contains(t, out, "client logs (this TUI)")
	assert.Contains(t, out, "hello from the client")
	assert.Contains(t, out, "second line")
}

func TestLogsView_LogrusOutputIsCaptured(t *testing.T) {
	m := New(context.Background(), "127.0.0.1:1")
	m.view = viewLogs
	m.width, m.height = 80, 24

	// Redirect logrus into the model's buffer as Run does, then restore.
	prev := logrus.StandardLogger().Out
	logrus.SetOutput(m.logs)
	defer logrus.SetOutput(prev)

	logrus.WithField("addr", "localhost:1").Info("launching horde TUI")

	assert.Contains(t, m.renderLogsView(), "launching horde TUI")
}

func TestScrollLogs_ClampsToBounds(t *testing.T) {
	m := New(context.Background(), "127.0.0.1:1")
	m.view = viewLogs
	m.width, m.height = 80, 24 // logsVisibleRows == 24 - logsChrome

	for i := 0; i < 40; i++ {
		_, err := m.logs.Write([]byte("line\n"))
		require.NoError(t, err)
	}

	// Scrolling up past the top clamps at the oldest visible window.
	m.scrollLogs(1000)
	maxScroll := m.logs.Len() - m.logsVisibleRows()
	assert.Equal(t, maxScroll, m.logScroll)

	// Scrolling back down past the newest clamps at 0.
	m.scrollLogs(-1000)
	assert.Equal(t, 0, m.logScroll)
}

func TestLogsView_ScrollShowsOlderLines(t *testing.T) {
	m := New(context.Background(), "127.0.0.1:1")
	m.view = viewLogs
	m.width, m.height = 80, 20

	for i := 0; i < 30; i++ {
		_, err := m.logs.Write([]byte("L" + string(rune('a'+i%26)) + "\n"))
		require.NoError(t, err)
	}
	lines := m.logs.Lines()
	newest := lines[len(lines)-1]

	// Pinned to the tail: the newest line is visible.
	assert.Contains(t, m.renderLogsView(), newest)

	// Scroll all the way back; the header notes the offset.
	m.scrollLogs(1000)
	out := m.renderLogsView()
	assert.True(t, strings.Contains(out, "scrolled +"))
	assert.Contains(t, out, lines[0], "oldest line visible when scrolled to top")
}
