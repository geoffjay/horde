package app

import (
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/sirupsen/logrus"

	"github.com/geoffjay/horde/internal/client"
)

// maxEvents bounds the in-model ring of cluster-activity events.
const maxEvents = 200

// eventsChrome is the number of rows reserved for the title, breadcrumb,
// footer, and edges when sizing the activity feed to the terminal height.
const eventsChrome = 8

// eventsFallbackRows is how many events to show before the first WindowSizeMsg.
const eventsFallbackRows = 20

// eventMsg carries one parsed event from the cluster-activity SSE stream.
type eventMsg struct{ ev client.Event }

// eventStreamEndMsg signals that the activity stream failed to open or ended.
type eventStreamEndMsg struct{ err error }

// goEvents navigates to the live cluster-activity feed, clearing the breadcrumb
// stack and opening the event stream.
func (m *Model) goEvents() tea.Cmd {
	m.unsubscribeAgentContext()
	m.unsubscribeInvoke()
	m.view = viewEvents
	m.crumbs = nil
	m.cursor = 0
	m.selectedProjectID = ""
	m.selectedAgentID = ""
	m.actionErr = ""
	return m.subscribeEvents()
}

// subscribeEvents opens the SSE cluster-activity stream. The server flushes the
// SSE headers immediately, so (like subscribeAgentContext) the open is done
// inline rather than in a command goroutine.
func (m *Model) subscribeEvents() tea.Cmd {
	m.unsubscribeEvents()

	ctx, cancel := context.WithCancel(m.ctx)
	m.eventsCancel = cancel

	ch, err := m.c.StreamEvents(ctx)
	if err != nil {
		cancel()
		m.eventsCancel = nil
		return func() tea.Msg { return eventStreamEndMsg{err: err} }
	}
	m.eventsCh = ch
	m.eventsConnected = true
	return m.pumpEvents()
}

// pumpEvents returns a tea.Cmd that reads one event from the SSE channel and
// returns it as an eventMsg. When the channel is closed it returns an
// eventStreamEndMsg so Update can clean up.
func (m *Model) pumpEvents() tea.Cmd {
	return func() tea.Msg {
		if m.eventsCh == nil {
			return eventStreamEndMsg{}
		}
		ev, ok := <-m.eventsCh
		if !ok {
			return eventStreamEndMsg{}
		}
		return eventMsg{ev: ev}
	}
}

// handleEventMsg appends an event to the bounded ring and re-arms the pump.
func (m *Model) handleEventMsg(msg eventMsg) (tea.Model, tea.Cmd) {
	m.events = append(m.events, msg.ev)
	if len(m.events) > maxEvents {
		m.events = m.events[len(m.events)-maxEvents:]
	}
	m.eventsConnected = true
	return m, m.pumpEvents() //nolint:gocritic // evalOrder: returning the cmd is the intended pattern
}

// handleEventStreamEnd cleans up activity-stream state when the stream closes
// or errors.
func (m *Model) handleEventStreamEnd(msg eventStreamEndMsg) (tea.Model, tea.Cmd) {
	m.eventsConnected = false
	m.eventsCh = nil
	if msg.err != nil {
		logrus.WithError(msg.err).Debug("tui: activity stream ended")
	}
	return m, nil
}

// unsubscribeEvents cancels the active activity SSE stream, if any.
func (m *Model) unsubscribeEvents() {
	if m.eventsCancel != nil {
		m.eventsCancel()
		m.eventsCancel = nil
	}
	m.eventsCh = nil
	m.eventsConnected = false
}

// renderEventsView renders the live cluster-activity feed: the most recent
// events newest-first, capped to what fits the terminal height.
func (m *Model) renderEventsView() string {
	if len(m.events) == 0 {
		return m.paint(lipgloss.NewStyle().Faint(true).Render, "  (waiting for cluster activity…)\n")
	}

	limit := eventsFallbackRows
	if m.height > 0 {
		limit = m.height - eventsChrome
		if limit < 1 {
			limit = 1
		}
	}

	var b strings.Builder
	shown := 0
	for i := len(m.events) - 1; i >= 0 && shown < limit; i-- {
		ev := m.events[i]
		fmt.Fprintf(&b, "  %s %-14s %-10s %s", eventDot(ev.Type), ev.Type, ev.Node, ev.AgentID)
		if ev.Name != "" {
			b.WriteString("  " + ev.Name)
		}
		b.WriteString("\n")
		shown++
	}
	return b.String()
}

// eventDot maps an event type to a status dot: green for spawn, red for exit,
// yellow for the transitional exiting state.
func eventDot(typ string) string {
	switch typ {
	case client.EventAgentSpawned:
		return greenDot()
	case client.EventAgentExiting:
		return yellowDot()
	case client.EventAgentExited:
		return redDot()
	default:
		return greyDot()
	}
}
