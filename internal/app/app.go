// Package app implements the horde TUI: a bubbletea program that is the
// primary interface for interacting with the horde system. The TUI is a
// pure client of the node API — it never starts a node in-process and never
// imports internal/server. If no node is reachable at the configured address
// it shows a 60-second retry countdown (with an immediate-retry key) rather
// than spawning one.
package app

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/sirupsen/logrus"

	"github.com/geoffjay/horde/internal/client"
)

// retryInterval is how long the TUI waits between automatic connection
// retries when no node is reachable.
const retryInterval = 60 * time.Second

// nodeFetchTimeout caps a single node-info/agents fetch.
const nodeFetchTimeout = 10 * time.Second

// agentRefreshInterval is how often the TUI re-fetches agents while connected.
const agentRefreshInterval = 2 * time.Second

// view identifies one screen in the breadcrumb drill-down stack.
type view int

const (
	// viewProjects is the projects home — the default connected view.
	viewProjects view = iota
	// viewProjectDetail is one project's team and per-agent context.
	viewProjectDetail
	// viewAgent is a single agent's live execution context (SSE).
	viewAgent
	// viewInvoke is the multi-turn conversation with an agent.
	viewInvoke
	// viewCluster is the cluster topology (nodes + remote agents).
	viewCluster
)

// breadcrumbEntry is one level of the drill-down stack. The view identifies
// the screen; id is the project or agent id (empty for top-level screens);
// label is the display name in the breadcrumb line.
type breadcrumbEntry struct {
	view  view
	id    string
	label string
}

// Model is the bubbletea model for the horde TUI.
type Model struct {
	ctx context.Context
	c   *client.Client

	// connection state
	connected   bool
	retrying    bool
	retryIn     time.Duration // remaining seconds until next auto-retry
	lastAttempt time.Time

	// node + agents
	node   client.NodeInfo
	agents []client.Agent

	// navigation: the current view and the breadcrumb stack of entries
	// pushed to reach it. The stack is empty for top-level screens
	// (projects, cluster); drill-down screens push entries so esc pops back.
	view   view
	crumbs []breadcrumbEntry

	// list cursor within the current view (index into the visible list)
	cursor int

	// selectedProjectID is the project open in the projectDetail/agent/invoke
	// views. Set by pushView when drilling in from the projects list; used to
	// look up the project independently of the cursor (which indexes team
	// agents in projectDetail).
	selectedProjectID string

	// cached domain state
	projects []client.Project
	contexts map[string]client.ExecutionContext
	nodes    client.ClusterView

	// status line + command palette overlay
	status *StatusLine
	pal    palette

	width    int
	height   int
	quitting bool
}

// New constructs the initial Model for the TUI, targeting the node API at
// addr (host:port).
func New(ctx context.Context, addr string) *Model {
	return &Model{
		ctx:      ctx,
		c:        client.New(addr),
		node:     client.NodeInfo{Mode: "unknown"},
		view:     viewProjects,
		contexts: make(map[string]client.ExecutionContext),
		status:   DefaultStatusLine(),
	}
}

// Init implements tea.Model.
func (m *Model) Init() tea.Cmd {
	return m.connect
}

// --- messages ---

type connectResultMsg struct {
	err error
}

type nodeInfoMsg struct {
	node     client.NodeInfo
	agents   []client.Agent
	projects []client.Project
	contexts map[string]client.ExecutionContext
	err      error
}

type tickMsg struct{}

type retryTickMsg struct{}

// --- commands ---

// connect probes the node's /health endpoint. On success it fetches node
// info + agents; on failure it arms the retry countdown.
func (m *Model) connect() tea.Msg {
	err := m.c.Health(m.ctx)
	return connectResultMsg{err: err}
}

// loadNode fetches node metadata, the agent list, the projects, and the
// execution contexts after a successful health check. Errors from
// secondary fetches (projects, contexts) are tolerated — the TUI shows
// what it got and leaves the rest empty.
func (m *Model) loadNode() tea.Msg {
	ctx, cancel := context.WithTimeout(m.ctx, nodeFetchTimeout)
	defer cancel()

	node, nErr := m.c.Node(ctx)
	agents, aErr := m.c.ListAgents(ctx)
	if nErr != nil && aErr != nil {
		return nodeInfoMsg{err: nErr}
	}

	// Projects and contexts are best-effort: a node that doesn't expose them
	// (e.g. an older version) should not prevent the TUI from rendering.
	// Errors are logged via logrus so forwarding issues are diagnosable.
	projects, pErr := m.c.ListProjects(ctx, "")
	if pErr != nil {
		logrus.WithError(pErr).Debug("tui: fetch projects failed")
	}
	contexts, cErr := m.c.ListAgentContexts(ctx)
	if cErr != nil {
		logrus.WithError(cErr).Debug("tui: fetch agent contexts failed")
	}
	ctxMap := make(map[string]client.ExecutionContext, len(contexts))
	for i := range contexts {
		ctxMap[contexts[i].AgentID] = contexts[i]
	}

	return nodeInfoMsg{
		node:     node,
		agents:   agents,
		projects: projects,
		contexts: ctxMap,
	}
}

// Update implements tea.Model.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case connectResultMsg:
		if msg.err == nil {
			m.connected = true
			m.retrying = false
			return m, m.loadNode
		}
		cmd := m.armRetry()
		return m, cmd

	case nodeInfoMsg:
		if msg.err != nil {
			cmd := m.armRetry()
			return m, cmd
		}
		m.node = msg.node
		m.agents = msg.agents
		if msg.projects != nil {
			m.projects = msg.projects
		}
		if msg.contexts != nil {
			m.contexts = msg.contexts
		}
		// periodic refresh of agents
		return m, tea.Tick(agentRefreshInterval, func(time.Time) tea.Msg { return tickMsg{} })

	case tickMsg:
		if !m.connected {
			return m, nil
		}
		return m, m.loadNode

	case retryTickMsg:
		if !m.retrying {
			return m, nil
		}
		m.retryIn -= time.Second
		if m.retryIn <= 0 {
			return m, m.connect
		}
		return m, tea.Tick(time.Second, func(time.Time) tea.Msg { return retryTickMsg{} })

	case tea.KeyPressMsg:
		return m.handleKey(msg)
	}

	return m, nil
}

// handleKey dispatches key presses. When the command palette is open all keys
// are routed to it (see handlePaletteKey). Otherwise "q"/ctrl+c quits, ctrl+p
// opens the palette, "r" refreshes when connected or triggers an immediate
// retry when in the retry state, and the arrow keys / enter / esc navigate
// the breadcrumb drill-down.
func (m *Model) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.pal.open {
		return m.handlePaletteKey(msg)
	}

	switch msg.String() {
	case "q", "ctrl+c":
		m.quitting = true
		return m, tea.Quit
	case "ctrl+p":
		m.openPalette()
		return m, nil
	case "r":
		if m.connected {
			return m, m.loadNode
		}
		// immediate retry
		m.retryIn = 0
		return m, m.connect
	}

	if !m.connected {
		return m, nil
	}

	switch msg.String() {
	case "esc":
		if len(m.crumbs) > 0 {
			m.popView()
			return m, nil
		}
	case "enter":
		return m.drillIn()
	case "up", "k":
		m.moveCursor(-1)
		return m, nil
	case "down", "j":
		m.moveCursor(1)
		return m, nil
	}
	return m, nil
}

// moveCursor moves the list cursor by delta, clamped to the visible list.
func (m *Model) moveCursor(delta int) {
	n := m.listLength()
	if n == 0 {
		m.cursor = 0
		return
	}
	m.cursor += delta
	if m.cursor < 0 {
		m.cursor = 0
	}
	if m.cursor >= n {
		m.cursor = n - 1
	}
}

// listLength returns the number of items in the current view's list.
func (m *Model) listLength() int {
	switch m.view {
	case viewProjects:
		return len(m.projects)
	case viewProjectDetail, viewAgent:
		return len(m.visibleAgents())
	case viewCluster:
		return len(m.nodes.Nodes)
	}
	return 0
}

// armRetry transitions the model into the retry-countdown state and returns
// a command that ticks once per second to drive the countdown.
func (m *Model) armRetry() tea.Cmd {
	m.connected = false
	m.retrying = true
	m.retryIn = retryInterval
	m.lastAttempt = time.Now()
	return tea.Tick(time.Second, func(time.Time) tea.Msg { return retryTickMsg{} })
}

// View implements tea.Model.
func (m *Model) View() tea.View {
	if m.quitting {
		return altView("Shutting down horde...\n")
	}

	background := m.fill(m.renderBody(), m.status.Render(m, m.innerWidth()))
	if !m.pal.open {
		return altView(background)
	}

	// Palette overlay: dim the whole background (it was rendered plain — see
	// paint) and composite the command dialog centered on top of it.
	dimmed := lipgloss.NewStyle().Faint(true).Foreground(dimColor).Render(background)
	dialog := m.renderPalette()
	x, y := m.paletteOffset(dialog)
	comp := lipgloss.NewCompositor(
		lipgloss.NewLayer(dimmed),
		lipgloss.NewLayer(dialog).X(x).Y(y).Z(1),
	)
	return altView(comp.Render())
}

// altView wraps content in a full-window (alternate-screen) view. The TUI
// always runs in the alt screen: the frame is a fixed full-terminal-height
// buffer, so the top-flush / full-height layout is anchored to the terminal
// and trailing blank rows (the bottom edge inset) actually render — unlike
// inline mode, which trims trailing blank lines.
func altView(content string) tea.View {
	v := tea.NewView(content)
	v.AltScreen = true
	return v
}

// dimColor is the foreground applied to the whole background while the command
// palette overlay is open.
var dimColor = lipgloss.Color("240")

// paint applies a style's render function to s, except while the command
// palette overlay is open, when it returns s unstyled. This lets View dim the
// entire background with a single faint wrapper: because the background carries
// no inner color/reset escapes, the wrapper applies uniformly. Callers pass a
// style's bound Render method (e.g. someStyle.Render) so the heavy Style struct
// is not copied by value.
func (m *Model) paint(render func(...string) string, s string) string {
	if m.pal.open {
		return s
	}
	return render(s)
}

// edgePad is the one-cell breathing room applied on the left, right, and
// bottom edges of the view. The top is intentionally flush, both because it
// reads fine and because padding it would complicate overlay positioning.
const edgePad = 1

// innerWidth is the usable content width after reserving the left/right edge
// padding.
func (m *Model) innerWidth() int {
	return max(m.width-edgePad-edgePad, 0)
}

// fill lays out the view so it occupies the full terminal height: the body is
// pinned to the top, the footer to the bottom, and the gap between them is
// padded with blank lines. The whole block is then inset by edgePad on the
// left, right, and bottom. Before the first WindowSizeMsg arrives (height 0)
// it falls back to a fixed single-line separator.
func (m *Model) fill(body, footer string) string {
	body = strings.TrimRight(body, "\n")
	if m.height <= 0 {
		return body + "\n\n" + footer + "\n"
	}
	// Reserve the bottom edge row (added by the padding below); the top stays
	// flush. Joining with N newlines yields N-1 blank rows between body and
	// footer, hence the +1 so the block is exactly m.height-edgePad rows tall.
	gap := m.height - edgePad - lipgloss.Height(body) - lipgloss.Height(footer) + 1
	if gap < 1 {
		gap = 1
	}
	inner := body + strings.Repeat("\n", gap) + footer
	return lipgloss.NewStyle().Padding(0, edgePad, edgePad, edgePad).Render(inner)
}

// renderBody builds the main content area (everything above the footer): the
// title plus either the retry panel or the current view's content. For
// connected views the second line is the breadcrumb (e.g. "projects ›
// auth-service › reviewer"); node mode/leader live in the status line.
func (m *Model) renderBody() string {
	var b strings.Builder
	title := m.paint(lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("212")).Render, "horde")
	b.WriteString(title + "\n\n")

	if !m.connected {
		b.WriteString(renderRetry(m))
		return b.String()
	}

	b.WriteString(m.renderBreadcrumb() + "\n\n")
	b.WriteString(m.renderView())
	return b.String()
}

// renderBreadcrumb builds the breadcrumb line from the crumb stack and the
// current view. Top-level screens show just their name ("projects" or
// "cluster"); drill-down screens show the full path joined by " › ".
func (m *Model) renderBreadcrumb() string {
	labels := make([]string, 0, len(m.crumbs)+1)
	for _, c := range m.crumbs {
		labels = append(labels, c.label)
	}
	labels = append(labels, m.currentViewLabel())
	bc := strings.Join(labels, " › ")
	return m.paint(lipgloss.NewStyle().Faint(true).Render, bc)
}

// currentViewLabel returns the breadcrumb label for the current view.
func (m *Model) currentViewLabel() string {
	switch m.view {
	case viewProjects:
		return "projects"
	case viewCluster:
		return "cluster"
	case viewProjectDetail:
		if i := m.selectedProjectIndex(); i >= 0 && i < len(m.projects) {
			return m.projects[i].Name
		}
		return "project"
	case viewAgent:
		if a, ok := m.selectedAgent(); ok {
			return a.Name
		}
		return "agent"
	case viewInvoke:
		return "invoke"
	}
	return ""
}

// renderView dispatches to the current view's renderer.
func (m *Model) renderView() string {
	switch m.view {
	case viewProjects:
		return m.renderProjectsView()
	case viewProjectDetail:
		return m.renderProjectDetailView()
	case viewAgent:
		return m.renderAgentView()
	case viewInvoke:
		return m.renderInvokeView()
	case viewCluster:
		return m.renderClusterView()
	}
	return ""
}

// renderRetry builds the "no server available" panel shown while the TUI
// waits to retry.
func renderRetry(m *Model) string {
	addr := m.c.BaseURL()
	secs := int(m.retryIn.Seconds())
	var b strings.Builder
	warnStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("203")).Bold(true)
	b.WriteString(m.paint(warnStyle.Render, "No horde node available"))
	b.WriteString("\n\n")
	fmt.Fprintf(&b, "The TUI could not reach a node at %s.\n", addr)
	fmt.Fprintf(&b, "Retrying in %ds...\n", secs)
	return b.String()
}

// Run launches the horde TUI. It blocks until the user quits.
func Run(ctx context.Context, addr string) error {
	m := New(ctx, addr)
	p := tea.NewProgram(m, tea.WithOutput(os.Stderr))
	if _, err := p.Run(); err != nil {
		return fmt.Errorf("tui run: %w", err)
	}
	return nil
}
