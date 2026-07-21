package app

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
)

// renderTeamsView renders the teams overview. Teams are defined per project
// (there is no standalone team API), so each row is a project with its team
// size. Selecting one opens the team roster (renderTeamDetailView).
func (m *Model) renderTeamsView() string {
	if len(m.projects) == 0 {
		return m.paint(lipgloss.NewStyle().Faint(true).Render, "  (no teams — teams are defined per project)\n")
	}
	var b strings.Builder
	for i, p := range m.projects {
		line := fmt.Sprintf("  %-20s %s", p.Name, agentCountLabel(len(p.Team.Agents)))
		if i == m.cursor && m.focus == focusDetail {
			line = selStyle().Render(line)
		}
		b.WriteString(line + "\n")
	}
	return b.String()
}

// renderTeamDetailView renders one project's team roster, reusing the project
// detail team-agent row so per-agent execution state is shown consistently.
func (m *Model) renderTeamDetailView() string {
	i := m.selectedProjectIndex()
	if i < 0 {
		return m.paint(lipgloss.NewStyle().Faint(true).Render, "  (team not found)\n")
	}
	p := m.projects[i]
	var b strings.Builder
	fmt.Fprintf(&b, "  team · %s\n\n", p.Name)
	if len(p.Team.Agents) == 0 {
		b.WriteString(m.paint(lipgloss.NewStyle().Faint(true).Render, "    (no agents assigned)\n"))
		return b.String()
	}
	for j, a := range p.Team.Agents {
		b.WriteString(m.renderTeamAgentRow(a, j == m.cursor && m.focus == focusDetail) + "\n")
	}
	return b.String()
}
