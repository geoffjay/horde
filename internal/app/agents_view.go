package app

import (
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/geoffjay/horde/internal/client"
)

// goAgents navigates to the top-level Agents list, clearing the breadcrumb
// stack and any drill-down selection.
func (m *Model) goAgents() {
	m.unsubscribeAgentContext()
	m.unsubscribeInvoke()
	m.unsubscribeEvents()
	m.view = viewAgents
	m.crumbs = nil
	m.cursor = 0
	m.selectedProjectID = ""
	m.selectedAgentID = ""
	m.actionErr = ""
}

// renderAgentsView lists the node's running agents with their status and active
// project (or "unassigned"), so a freshly-created agent is visible and can be
// assigned or invoked.
func (m *Model) renderAgentsView() string {
	if len(m.agents) == 0 {
		return m.paint(lipgloss.NewStyle().Faint(true).Render, "  (no agents on this node — create one with the palette's \"New Agent\")\n")
	}

	var b strings.Builder
	for i, a := range m.agents {
		project := m.contexts[a.ID].Project
		placement := dimLabel("unassigned")
		if project != "" {
			placement = project
		}
		dot := greyDot()
		switch client.AgentState(a.Status) {
		case client.AgentRunning:
			dot = greenDot()
		case client.AgentExiting:
			dot = yellowDot()
		case client.AgentExited:
			dot = redDot()
		}
		line := fmt.Sprintf("  %s %-14s %-12s %s", dot, a.ID, a.Name, placement)
		if i == m.cursor {
			line = selStyle().Render(line)
		}
		b.WriteString(line + "\n")
	}
	return b.String()
}

// dimLabel renders faint secondary text (small helper local to this view).
func dimLabel(s string) string {
	return lipgloss.NewStyle().Faint(true).Render(s)
}

// attachAgentCmd returns a tea.Cmd that attaches an existing agent to a project
// by id and refreshes. The result flows through the shared projectActionMsg
// handler.
func (m *Model) attachAgentCmd(projectID, agentID string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.ctx, nodeFetchTimeout)
		defer cancel()
		p, err := m.c.AttachAgent(ctx, projectID, agentID)
		return projectActionMsg{project: p, err: err}
	}
}
