package app

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
)

// renderUsersView renders the per-user identity list + the node's auth-enabled
// state. Tokens are never surfaced (the API never returns them). When auth is
// disabled the view renders a "disabled" header line and an empty list; when
// enabled, each user is listed with an admin badge and a "(you)" marker for
// the current identity. Fetch is best-effort: an older node without /users
// leaves the cached response empty, which renders as the disabled placeholder
// (m.users defaults to the zero value, AuthEnabled=false).
func (m *Model) renderUsersView() string {
	var b strings.Builder

	if !m.users.AuthEnabled {
		b.WriteString(m.paint(lipgloss.NewStyle().Faint(true).Render,
			"  Per-user auth is disabled (no auth.users configured).\n"+
				"  The node API is unauthenticated. Add an auth.users block to enable.\n"))
		return b.String()
	}

	b.WriteString(m.paint(lipgloss.NewStyle().Bold(true).Render, "  auth enabled"))
	b.WriteString("\n\n")

	if len(m.users.Users) == 0 {
		b.WriteString(m.paint(lipgloss.NewStyle().Faint(true).Render, "  (no users configured)\n"))
		return b.String()
	}

	for i := range m.users.Users {
		u := &m.users.Users[i]
		line := fmt.Sprintf("  %-16s", u.ID)
		if u.Admin {
			line += "  admin"
		}
		if u.You {
			line += "  (you)"
		}
		if i == m.cursor && m.focus == focusDetail {
			line = selStyle().Render(line)
		}
		b.WriteString(line + "\n")
	}

	return b.String()
}
