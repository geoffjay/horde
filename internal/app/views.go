package app

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/geoffjay/horde/internal/client"
)

// renderProjectsView renders the projects home — the default connected
// view. Each row shows a status dot, the project name, state, agent count,
// and goal. The cursor highlights the selected row. A rollup line below
// the list summarizes the total agents and their activity breakdown.
func (m *Model) renderProjectsView() string {
	if len(m.projects) == 0 {
		return m.paint(lipgloss.NewStyle().Faint(true).Render, "  (no projects)\n")
	}
	var b strings.Builder
	for i, p := range m.projects {
		dot := stateDot(p.State)
		line := fmt.Sprintf("  %s  %-20s %-10s %-9s  %s", dot, p.Name, p.State, agentCountLabel(len(p.Team.Agents)), p.Goal)
		if i == m.cursor {
			line = selStyle().Render(line)
		}
		b.WriteString(line + "\n")
	}
	b.WriteString("\n")
	b.WriteString(m.paint(lipgloss.NewStyle().Faint(true).Render, m.activityRollup()))
	b.WriteString("\n")
	return b.String()
}

// renderProjectDetailView renders one project's team and per-agent state.
// Each team agent row shows a status dot (from the agent's execution
// context activity), the agent name, activity, issue, turn id, and a
// trailing summary of errors and pending approvals. The cursor
// highlights the selected agent row.
func (m *Model) renderProjectDetailView() string {
	i := m.selectedProjectIndex()
	if i < 0 {
		return m.paint(lipgloss.NewStyle().Faint(true).Render, "  (project not found)\n")
	}
	p := m.projects[i]
	var b strings.Builder
	fmt.Fprintf(&b, "  %-10s %s\n", "state", p.State)
	if p.Workspace != "" {
		fmt.Fprintf(&b, "  %-10s %s\n", "workspace", p.Workspace)
	}
	if p.Goal != "" {
		fmt.Fprintf(&b, "  %-10s %s\n\n", "goal", p.Goal)
	} else {
		b.WriteString("\n")
	}

	b.WriteString("  Team\n")
	if len(p.Team.Agents) == 0 {
		b.WriteString(m.paint(lipgloss.NewStyle().Faint(true).Render, "    (no agents assigned)\n"))
		return b.String()
	}
	for j, a := range p.Team.Agents {
		line := m.renderTeamAgentRow(a, j == m.cursor)
		b.WriteString(line + "\n")
	}
	return b.String()
}

// renderTeamAgentRow renders one agent row in the project detail team list.
// It shows a status dot, the agent name, activity, issue, turn id, and a
// trailing note with error/approval counts from the execution context.
func (m *Model) renderTeamAgentRow(a client.TeamAgent, selected bool) string {
	ctx, hasCtx := m.contexts[a.AgentID]
	activity := "—"
	dot := greyDot()
	issue := ""
	turn := ""
	trail := ""

	if hasCtx {
		activity = string(ctx.Activity)
		dot = stateDot(activity)
		issue = ctx.Issue
		turn = formatTurn(ctx.TurnID)
		trail = contextTrail(&ctx)
	}

	line := fmt.Sprintf("    %s  %-16s %-10s", dot, a.Name, activity)
	if issue != "" {
		line += fmt.Sprintf("  %-20s", issue)
	}
	if turn != "" {
		line += "  " + turn
	}
	if trail != "" {
		line += "  " + trail
	}
	if selected {
		line = selStyle().Render(line)
	}
	return line
}

// formatTurn returns "turn <id>" for a non-empty turn id, or "".
func formatTurn(id string) string {
	if id == "" {
		return ""
	}
	return "turn " + id
}

// contextTrail builds the trailing note for an agent row: error count,
// approval count, and blocked reason, joined by " · ".
func contextTrail(ctx *client.ExecutionContext) string {
	errCount := len(ctx.Errors)
	if errCount == 0 && ctx.ErrorCount > 0 {
		errCount = ctx.ErrorCount
	}
	apprCount := len(ctx.PendingApprovals)
	if apprCount == 0 && ctx.PendingApprovalCount > 0 {
		apprCount = ctx.PendingApprovalCount
	}
	var notes []string
	if errCount > 0 {
		notes = append(notes, fmt.Sprintf("%d error%s", errCount, pluralS(errCount)))
	}
	if apprCount > 0 {
		notes = append(notes, fmt.Sprintf("%d approval%s", apprCount, pluralS(apprCount)))
	}
	if ctx.Blocked && ctx.BlockedReason != "" {
		notes = append(notes, ctx.BlockedReason)
	}
	return strings.Join(notes, " · ")
}

// pluralS returns "s" when n != 1, "" otherwise — for simple pluralization.
func pluralS(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// renderAgentView renders a single agent's live execution context. The
// SSE subscription (subscribeAgentContext) updates m.contexts in real
// time; this renderer reads the cached snapshot. The layout matches the
// plan mockup: header line with name/id/status, project+issue, activity,
// blocked reason, pending approvals, errors, and note.
func (m *Model) renderAgentView() string {
	a, ok := m.selectedAgent()
	if !ok {
		return m.paint(lipgloss.NewStyle().Faint(true).Render, "  (no agent selected)\n")
	}
	ctx := m.contexts[a.ID]

	// Header: name + [id] + status dot + lifecycle + turn
	dot := stateDot(string(ctx.Activity))
	statusLabel := string(ctx.Activity)
	if ctx.Blocked {
		dot = redDot()
		statusLabel = blockedLabel
	}
	header := fmt.Sprintf("  %-12s %s [%s]   %s %s", "agent", a.Name, a.ID, dot, statusLabel)
	if ctx.Lifecycle != "" {
		header += fmt.Sprintf("          lifecycle %s", ctx.Lifecycle)
	}
	if ctx.TurnID != "" {
		header += fmt.Sprintf(" · turn %s", ctx.TurnID)
	}

	var b strings.Builder
	b.WriteString(header + "\n")

	// Project + issue
	projLine := fmt.Sprintf("  %-12s %s", "project", ctx.Project)
	if ctx.Issue != "" {
		projLine += " · issue " + ctx.Issue
	}
	b.WriteString(projLine + "\n")

	// Activity + waiting on model
	activityLine := fmt.Sprintf("  %-12s %s", "activity", ctx.Activity)
	if ctx.WaitingModel {
		activityLine += " · waiting on model: yes"
	} else {
		activityLine += " · waiting on model: no"
	}
	b.WriteString(activityLine + "\n")

	// Blocked reason
	if ctx.Blocked {
		reason := ctx.BlockedReason
		if reason == "" {
			reason = blockedLabel
		}
		fmt.Fprintf(&b, "  %-12s %s\n", "blocked", reason)
	}

	// Pending approvals and errors
	b.WriteString(renderAgentApprovals(&ctx))
	b.WriteString(renderAgentErrors(&ctx))

	// Note
	if ctx.Note != "" {
		fmt.Fprintf(&b, "\n  note  %s\n", ctx.Note)
	}

	return b.String()
}

// renderAgentApprovals renders the pending approvals section for the agent
// view, handling both full (local) and redacted (remote) contexts.
func renderAgentApprovals(ctx *client.ExecutionContext) string {
	var b strings.Builder
	if len(ctx.PendingApprovals) > 0 {
		fmt.Fprintf(&b, "\n  Pending approvals (%d)\n", len(ctx.PendingApprovals))
		for _, ap := range ctx.PendingApprovals {
			fmt.Fprintf(&b, "    ▸ %-20s req %s\n", ap.ToolName, truncateID(ap.RequestID))
		}
	} else if ctx.PendingApprovalCount > 0 {
		fmt.Fprintf(&b, "\n  Pending approvals (%d)\n", ctx.PendingApprovalCount)
	}
	return b.String()
}

// renderAgentErrors renders the errors section for the agent view, handling
// both full (local) and redacted (remote) contexts.
func renderAgentErrors(ctx *client.ExecutionContext) string {
	errCount := len(ctx.Errors)
	if errCount == 0 && ctx.ErrorCount > 0 {
		errCount = ctx.ErrorCount
	}
	if errCount == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "\n  Errors (%d)\n", errCount)
	for _, e := range ctx.Errors {
		fatal := ""
		if e.Fatal {
			fatal = "   fatal"
		}
		fmt.Fprintf(&b, "    ✗ %-12s %s%s\n", e.Code, e.Message, fatal)
	}
	return b.String()
}

// truncateIDLen is the number of leading characters to keep when truncating
// an id for display.
const truncateIDLen = 7

// blockedLabel is the status label shown when an agent is blocked.
const blockedLabel = "blocked"

// truncateID shortens an id for display (first truncateIDLen chars + "…"),
// matching the plan mockup's "req 7c2e…" style.
func truncateID(id string) string {
	if len(id) <= truncateIDLen {
		return id
	}
	return id[:truncateIDLen] + "…"
}

// renderInvokeView renders the multi-turn conversation: a session banner,
// the transcript of user/agent messages, and a text input field at the
// bottom. When streaming, a "streaming ●" indicator appears next to the
// input. A 409 (project paused) renders as a red error notice in place
// of the input.
func (m *Model) renderInvokeView() string {
	a, ok := m.selectedAgent()
	if !ok {
		return m.paint(lipgloss.NewStyle().Faint(true).Render, "  (no agent selected)\n")
	}

	var b strings.Builder

	// Session banner
	projectID := m.selectedProjectID
	b.WriteString(m.paint(lipgloss.NewStyle().Faint(true).Render,
		fmt.Sprintf("  session %s:%s · multi-turn\n\n", a.Name, projectID)))

	// Transcript
	for _, e := range m.invokeTranscript {
		switch e.role {
		case "user":
			fmt.Fprintf(&b, "  › you\n    %s\n\n", e.text)
		case roleAgent:
			dot := greenDot()
			fmt.Fprintf(&b, "  %s %s\n    %s\n\n", dot, a.Name, e.text)
		}
	}

	// Error notice (e.g. 409 project paused)
	if m.invokeErr != "" {
		errStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
		b.WriteString(m.paint(errStyle.Render, "  ✗ "+m.invokeErr+"\n\n"))
	}

	// Input field with streaming indicator
	if m.invokeStreaming {
		dot := lipgloss.NewStyle().Foreground(lipgloss.Color("42")).Render("●")
		fmt.Fprintf(&b, "  ›_%s %s\n", m.invokeInput, dot)
		b.WriteString(m.paint(lipgloss.NewStyle().Faint(true).Render, "    streaming ●\n"))
	} else if m.invokeErr == "" {
		cursor := lipgloss.NewStyle().Reverse(true).Render(" ")
		fmt.Fprintf(&b, "  ›_%s\n", m.invokeInput+cursor)
		b.WriteString(m.paint(lipgloss.NewStyle().Faint(true).Render, "    enter send · esc back\n"))
	}

	return b.String()
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

// activityRollup produces the summary line under the projects list: total
// agents across all projects and a breakdown by activity state (idle, busy,
// blocked), derived from the cached execution contexts. Matches the plan
// mockup format: "4 agents · 1 idle · 2 busy · 1 blocked".
func (m *Model) activityRollup() string {
	totalAgents := 0
	for _, p := range m.projects {
		totalAgents += len(p.Team.Agents)
	}
	if totalAgents == 0 {
		return "0 agents"
	}

	var idle, busy, blocked int
	//nolint:gocritic // map iteration copies values; context is 240 bytes, negligible at this scale
	for _, ctx := range m.contexts {
		switch {
		case ctx.Blocked:
			blocked++
		case ctx.Activity == client.StateBusy:
			busy++
		case ctx.Activity == client.StateIdle:
			idle++
		}
	}

	parts := []string{agentCountLabel(totalAgents)}
	if idle > 0 {
		parts = append(parts, fmt.Sprintf("%d idle", idle))
	}
	if busy > 0 {
		parts = append(parts, fmt.Sprintf("%d busy", busy))
	}
	if blocked > 0 {
		parts = append(parts, fmt.Sprintf("%d blocked", blocked))
	}
	return strings.Join(parts, " · ")
}
