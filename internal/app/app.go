// Package app implements the horde TUI: a bubbletea program that is the
// primary interface for interacting with the horde system, its server, and
// its agents.
package app

import (
	"context"
	"fmt"
	"os"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/geoffjay/horde/internal/server"
)

// Model is the bubbletea model for the horde TUI.
type Model struct {
	ctx      context.Context
	srv      *server.Server
	width    int
	height   int
	agents   []server.AgentInfo
	quitting bool
}

// New constructs the initial Model for the TUI.
func New(ctx context.Context, srv *server.Server) *Model {
	return &Model{
		ctx:    ctx,
		srv:    srv,
		agents: srv.Agents(),
	}
}

// Init implements tea.Model.
func (m *Model) Init() tea.Cmd {
	return refreshAgents(m)
}

// refreshAgents polls the server's running agents and returns a command that
// delivers the result as a message.
func refreshAgents(m *Model) tea.Cmd {
	return func() tea.Msg {
		return agentsUpdatedMsg{agents: m.srv.Agents()}
	}
}

// agentsUpdatedMsg carries a fresh snapshot of running agents.
type agentsUpdatedMsg struct {
	agents []server.AgentInfo
}

// Update implements tea.Model.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case agentsUpdatedMsg:
		m.agents = msg.agents
		return m, nil

	case tea.KeyPressMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			m.quitting = true
			return m, tea.Quit
		case "r":
			return m, refreshAgents(m)
		case "s":
			return m, m.spawnAgent
		case "x":
			return m, m.quitServer
		}
	}

	return m, nil
}

// spawnAgent spawns the default greeter agent on the server.
func (m *Model) spawnAgent() tea.Msg {
	_, _ = m.srv.SpawnAgent(m.ctx, "greeter")
	return agentsUpdatedMsg{agents: m.srv.Agents()}
}

// quitServer signals the server context to stop and quits the TUI.
func (m *Model) quitServer() tea.Msg {
	if cancel, ok := m.ctx.Value(cancelKey{}).(context.CancelFunc); ok {
		cancel()
	}
	return tea.QuitMsg{}
}

// cancelKey is a context value key used to stash a CancelFunc so the TUI can
// stop the server.
type cancelKey struct{}

// WithCancel stores a CancelFunc in the context so the TUI can stop the
// server it is bound to.
func WithCancel(ctx context.Context, cancel context.CancelFunc) context.Context {
	return context.WithValue(ctx, cancelKey{}, cancel)
}

// View implements tea.Model.
func (m *Model) View() tea.View {
	if m.quitting {
		return tea.NewView("Shutting down horde...\n")
	}

	var b strings.Builder
	title := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("212")).Render("horde")
	b.WriteString(title + "\n\n")

	modeStyle := lipgloss.NewStyle().Faint(true)
	b.WriteString(modeStyle.Render(fmt.Sprintf("mode: %s", m.srv.Mode())))
	if m.srv.LeaderConnected() {
		b.WriteString(modeStyle.Render("  • leader connected"))
	}
	b.WriteString("\n\n")

	b.WriteString("Running agents:\n")
	if len(m.agents) == 0 {
		b.WriteString("  (none)\n")
	}
	for _, a := range m.agents {
		line := fmt.Sprintf("  • %s  [%s]", a.Name, a.ID)
		b.WriteString(line + "\n")
	}

	b.WriteString("\n")
	helpStyle := lipgloss.NewStyle().Faint(true)
	b.WriteString(helpStyle.Render("[r] refresh  [s] spawn agent  [x] stop server  [q] quit"))
	b.WriteString("\n")

	return tea.NewView(b.String())
}

// Run launches the horde TUI. It blocks until the user quits.
func Run(ctx context.Context, srv *server.Server) error {
	m := New(ctx, srv)
	p := tea.NewProgram(m, tea.WithOutput(os.Stderr))
	if _, err := p.Run(); err != nil {
		return fmt.Errorf("tui run: %w", err)
	}
	return nil
}
