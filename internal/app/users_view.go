package app

import "charm.land/lipgloss/v2"

// renderUsersView renders the (placeholder) users overview. Per-user accounts
// require authentication support, which does not exist yet, so the group is
// present in the sidebar for completeness but has no data to show.
func (m *Model) renderUsersView() string {
	return m.paint(lipgloss.NewStyle().Faint(true).Render,
		"  User management is not available yet.\n"+
			"  Per-user accounts arrive with authentication support.\n")
}
