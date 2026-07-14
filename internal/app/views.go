package app

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/geoffjay/horde/internal/client"
)

// renderProjectsView renders the projects home — the default connected
// view. Each row shows a status dot, the project name, state, agent count,
// and goal. The cursor highlights the selected row.
func (m *Model) renderProjectsView() string {
	if len(m.projects) == 0 {
		return m.paint(lipgloss.NewStyle().Faint(true).Render, "  (no projects)\n")
	}
	var b strings.Builder
	for i, p := range m.projects {
		dot := stateDot(p.State)
		line := fmt.Sprintf("  %s  %-20s %-10s %d agents   %s", dot, p.Name, p.State, len(p.Team.Agents), p.Goal)
		if i == m.cursor {
			line = selStyle().Render(line)
		}
		b.WriteString(line + "\n")
	}
	b.WriteString("\n")
	b.WriteString(m.paint(lipgloss.NewStyle().Faint(true).Render, projectRollup(m.projects)))
	b.WriteString("\n")
	return b.String()
}

// renderProjectDetailView renders one project's team and per-agent state.
func (m *Model) renderProjectDetailView() string {
	i := m.selectedProjectIndex()
	if i < 0 {
		return m.paint(lipgloss.NewStyle().Faint(true).Render, "  (project not found)\n")
	}
	p := m.projects[i]
	var b strings.Builder
	fmt.Fprintf(&b, "  %-10s %s\n", "state", p.State)
	fmt.Fprintf(&b, "  %-10s %s\n", "workspace", p.Workspace)
	fmt.Fprintf(&b, "  %-10s %s\n\n", "goal", p.Goal)
	b.WriteString("  Team\n")
	if len(p.Team.Agents) == 0 {
		b.WriteString(m.paint(lipgloss.NewStyle().Faint(true).Render, "    (no agents assigned)\n"))
	}
	for j, a := range p.Team.Agents {
		ctx, ok := m.contexts[a.AgentID]
		activity := "—"
		if ok {
			activity = string(ctx.Activity)
		}
		line := fmt.Sprintf("    %s  %-16s %-10s", stateDot(activity), a.Name, activity)
		if j == m.cursor {
			line = selStyle().Render(line)
		}
		b.WriteString(line + "\n")
	}
	return b.String()
}

// renderAgentView renders a single agent's execution context. In slice 2
// this is a static snapshot from the cached contexts map; the live SSE
// subscription arrives in a later slice.
func (m *Model) renderAgentView() string {
	a, ok := m.selectedAgent()
	if !ok {
		return m.paint(lipgloss.NewStyle().Faint(true).Render, "  (no agent selected)\n")
	}
	ctx := m.contexts[a.ID]
	var b strings.Builder
	fmt.Fprintf(&b, "  %-10s %s [%s]\n", "agent", a.Name, a.ID)
	fmt.Fprintf(&b, "  %-10s %s · %s\n", "project", ctx.Project, ctx.Issue)
	fmt.Fprintf(&b, "  %-10s %s\n", "activity", ctx.Activity)
	if ctx.Blocked {
		fmt.Fprintf(&b, "  %-10s %s\n", "blocked", ctx.BlockedReason)
	}
	if len(ctx.Errors) > 0 {
		b.WriteString("\n  Errors\n")
		for _, e := range ctx.Errors {
			fmt.Fprintf(&b, "    ✗ %s  %s\n", e.Code, e.Message)
		}
	}
	if len(ctx.PendingApprovals) > 0 {
		b.WriteString("\n  Pending approvals\n")
		for _, ap := range ctx.PendingApprovals {
			fmt.Fprintf(&b, "    ▸ %s  %s\n", ap.ToolName, ap.RequestID)
		}
	}
	return b.String()
}

// renderInvokeView renders the multi-turn conversation. In slice 2 this is
// a placeholder; the SSE invoke stream and message input arrive in a
// later slice.
func (m *Model) renderInvokeView() string {
	return m.paint(lipgloss.NewStyle().Faint(true).Render,
		"  (invoke screen — streaming conversation arrives in a later slice)\n")
}

// renderClusterView renders the cluster topology: the leader, all nodes,
// and their last-seen/staleness.
func (m *Model) renderClusterView() string {
	var b strings.Builder
	leader := m.nodes.LeaderID
	if leader == "" {
		leader = m.node.NodeID
	}
	fmt.Fprintf(&b, "  leader  %s", leader)
	if leader == m.node.NodeID {
		b.WriteString("  (this node)")
	}
	b.WriteString("\n\n")
	if len(m.nodes.Nodes) == 0 {
		b.WriteString(m.paint(lipgloss.NewStyle().Faint(true).Render, "  (no other nodes registered)\n"))
		return b.String()
	}
	for i, n := range m.nodes.Nodes {
		dot := "●"
		if n.Stale {
			dot = "◐"
		}
		line := fmt.Sprintf("  %s  %-8s %-20s %-6d agents", dot, n.NodeID, n.Addr, len(n.Agents))
		if n.Stale {
			line += "  stale"
		}
		if i == m.cursor {
			line = selStyle().Render(line)
		}
		b.WriteString(line + "\n")
	}
	return b.String()
}

// stateDot returns the colored status glyph for a project state or agent
// activity.
func stateDot(state string) string {
	switch strings.ToLower(state) {
	case "active", "idle", "running":
		return greenDot()
	case "busy", "paused", "waiting":
		return yellowDot()
	case "blocked", "error", "exited":
		return redDot()
	case "finished":
		return greyDot()
	}
	return greyDot()
}

// colored status dots. These use lipgloss colors matching the plan's legend:
// green(42) active/idle, yellow busy/paused/waiting, red(203) blocked/error,
// grey finished/exited.
func greenDot() string  { return lipglossColor("42", "●") }
func yellowDot() string { return lipglossColor("203", "◐") }
func redDot() string    { return lipglossColor("203", "▲") }
func greyDot() string   { return lipglossColor("240", "○") }

// lipglossColor wraps s in a foreground color style.
func lipglossColor(color, s string) string {
	return lipgloss.NewStyle().Foreground(lipgloss.Color(color)).Render(s)
}

// selStyle is the highlight style for the cursor row in list views.
func selStyle() lipglossStyle {
	return lipgloss.NewStyle().Background(lipgloss.Color("62")).Foreground(lipgloss.Color("255"))
}

// lipglossStyle is an alias to avoid importing lipgloss in every file that
// uses selStyle. It matches lipgloss.Style.
type lipglossStyle = lipgloss.Style

// projectRollup produces the summary line under the projects list: total
// agents and counts by activity state.
func projectRollup(projects []client.Project) string {
	totalAgents := 0
	for _, p := range projects {
		totalAgents += len(p.Team.Agents)
	}
	return fmt.Sprintf("  %d agents across %d projects", totalAgents, len(projects))
}
