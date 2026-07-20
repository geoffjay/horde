package app

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
)

// logsChrome is the number of rows reserved for the title, breadcrumb, header,
// footer, and edges when sizing the logs page to the terminal height.
const logsChrome = 9

// logsFallbackRows is how many log lines to show before the first
// WindowSizeMsg sets the terminal height.
const logsFallbackRows = 20

// logsUpdatedMsg is delivered (via the program) when the client-log buffer
// gains lines. It carries no data and has no Update case: bubbletea re-renders
// after every Update, so merely delivering it keeps the logs page live.
type logsUpdatedMsg struct{}

// goLogs navigates to the client-log page, clearing the breadcrumb stack and
// resetting the scroll offset to the newest line.
func (m *Model) goLogs() {
	m.unsubscribeAgentContext()
	m.unsubscribeInvoke()
	m.unsubscribeEvents()
	m.view = viewLogs
	m.crumbs = nil
	m.cursor = 0
	m.logScroll = 0
	m.selectedProjectID = ""
	m.selectedAgentID = ""
	m.actionErr = ""
}

// logsVisibleRows returns how many log lines fit the current terminal height.
func (m *Model) logsVisibleRows() int {
	if m.height <= 0 {
		return logsFallbackRows
	}
	rows := m.height - logsChrome
	if rows < 1 {
		return 1
	}
	return rows
}

// scrollLogs adjusts the scroll offset by delta (positive scrolls back into
// history), clamped so the window never runs past either end of the buffer.
func (m *Model) scrollLogs(delta int) {
	total := 0
	if m.logs != nil {
		total = m.logs.Len()
	}
	maxScroll := total - m.logsVisibleRows()
	if maxScroll < 0 {
		maxScroll = 0
	}
	m.logScroll += delta
	if m.logScroll < 0 {
		m.logScroll = 0
	}
	if m.logScroll > maxScroll {
		m.logScroll = maxScroll
	}
}

// renderLogsView renders the TUI's own log output — the client-side logs
// captured into the in-memory buffer instead of stderr. Lines are shown
// oldest-to-newest (console order), windowed to what fits the terminal and
// offset by logScroll (0 = pinned to the newest line).
func (m *Model) renderLogsView() string {
	faint := lipgloss.NewStyle().Faint(true)
	var lines []string
	if m.logs != nil {
		lines = m.logs.Lines()
	}
	if len(lines) == 0 {
		return m.paint(faint.Render, "  (no client log output yet)\n")
	}

	limit := m.logsVisibleRows()
	// Re-clamp the offset in case the buffer shrank (eviction) or the window
	// grew since the last scroll.
	m.scrollLogs(0)

	// The window ends `logScroll` lines back from the newest line.
	end := len(lines) - m.logScroll
	if end > len(lines) {
		end = len(lines)
	}
	start := end - limit
	if start < 0 {
		start = 0
	}

	var b strings.Builder
	header := fmt.Sprintf("  client logs (this TUI) · %d lines", len(lines))
	if m.logScroll > 0 {
		header += fmt.Sprintf(" · scrolled +%d", m.logScroll)
	}
	b.WriteString(m.paint(faint.Render, header) + "\n")
	for _, line := range lines[start:end] {
		b.WriteString("  " + line + "\n")
	}
	return b.String()
}
