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

// Agent-form field indices, in display order: an agent-type selector and a node
// placement selector. Both are selectors (←→) — there is no free-text name, so
// only valid, node-known agent types can be chosen.
const (
	agentFormFieldAgent = iota
	agentFormFieldNode
	agentFormFieldCount
)

// nodeLocal is the placement selector value meaning "spawn on this node"; it
// maps to an empty node in the spawn request.
const nodeLocal = "local"

// agentForm is the state of the new-agent modal overlay: whether it is open,
// which field has focus, and the selected agent-type and node indices.
type agentForm struct {
	open     bool
	cursor   int
	agentIdx int
	nodeIdx  int
}

// agentActionMsg carries the result of spawning an agent from the new-agent
// form.
type agentActionMsg struct {
	agent client.Agent
	err   error
}

// openAgentForm shows the new-agent modal with cleared selections.
func (m *Model) openAgentForm() {
	m.agentForm = agentForm{open: true}
	m.actionErr = ""
}

// closeAgentForm hides the new-agent modal.
func (m *Model) closeAgentForm() {
	m.agentForm = agentForm{}
}

// agentNodeOptions returns the placement choices: "local", "auto", then each
// known node id (this node first, then registered slaves), de-duplicated.
func (m *Model) agentNodeOptions() []string {
	opts := []string{nodeLocal, "auto"}
	seen := map[string]bool{}
	if m.node.NodeID != "" {
		opts = append(opts, m.node.NodeID)
		seen[m.node.NodeID] = true
	}
	for _, n := range m.nodes.Nodes {
		if n.NodeID != "" && !seen[n.NodeID] {
			opts = append(opts, n.NodeID)
			seen[n.NodeID] = true
		}
	}
	return opts
}

// handleAgentFormKey handles key presses while the new-agent form is open:
// esc cancels, enter submits, tab/up/down move between the two selectors, and
// left/right cycle the focused selector.
func (m *Model) handleAgentFormKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case keyQuit:
		m.closeAgentForm()
		m.quitting = true
		return m, tea.Quit
	case keyEsc:
		m.closeAgentForm()
		return m, nil
	case keyEnter:
		return m.submitAgentForm()
	case "tab", keyDown:
		m.agentForm.cursor = (m.agentForm.cursor + 1) % agentFormFieldCount
		return m, nil
	case "shift+tab", "up":
		m.agentForm.cursor = (m.agentForm.cursor - 1 + agentFormFieldCount) % agentFormFieldCount
		return m, nil
	case "left":
		m.cycleAgentField(-1)
		return m, nil
	case "right":
		m.cycleAgentField(1)
		return m, nil
	}
	return m, nil
}

// cycleAgentField advances the focused selector by delta, wrapping around.
func (m *Model) cycleAgentField(delta int) {
	if m.agentForm.cursor == agentFormFieldNode {
		if n := len(m.agentNodeOptions()); n > 0 {
			m.agentForm.nodeIdx = (m.agentForm.nodeIdx + delta + n) % n
		}
		return
	}
	if n := len(m.availableAgents); n > 0 {
		m.agentForm.agentIdx = (m.agentForm.agentIdx + delta + n) % n
	}
}

// submitAgentForm spawns the selected agent type on the selected node. It is a
// no-op when no agent types are available (nothing valid to spawn). "local"
// maps to an empty node (spawn here).
func (m *Model) submitAgentForm() (tea.Model, tea.Cmd) {
	if len(m.availableAgents) == 0 || m.agentForm.agentIdx >= len(m.availableAgents) {
		return m, nil
	}
	name := m.availableAgents[m.agentForm.agentIdx].Name

	opts := m.agentNodeOptions()
	node := nodeLocal
	if m.agentForm.nodeIdx >= 0 && m.agentForm.nodeIdx < len(opts) {
		node = opts[m.agentForm.nodeIdx]
	}
	m.closeAgentForm()
	return m, m.spawnAgentCmd(name, node) //nolint:gocritic // evalOrder: returning the cmd is the intended pattern
}

// spawnAgentCmd returns a tea.Cmd that POSTs a new agent with the chosen
// placement and returns an agentActionMsg with the result.
func (m *Model) spawnAgentCmd(name, node string) tea.Cmd {
	placement := node
	if placement == nodeLocal {
		placement = ""
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.ctx, nodeFetchTimeout)
		defer cancel()
		a, err := m.c.SpawnAgent(ctx, name, placement)
		return agentActionMsg{agent: a, err: err}
	}
}

// handleAgentAction processes the spawn result: on error it surfaces the
// message in the body; either way it refreshes so a new agent appears.
func (m *Model) handleAgentAction(msg *agentActionMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.actionErr = "spawn agent: " + msg.err.Error()
		logrus.WithError(msg.err).Debug("tui: spawn agent failed")
	} else {
		m.actionErr = ""
	}
	return m, m.loadNode
}

// renderAgentForm builds the new-agent modal shown while the form is open,
// mirroring renderForm (its own layer over the dimmed background).
func (m *Model) renderAgentForm() string {
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("212"))
	faint := lipgloss.NewStyle().Faint(true)

	agentVal := faint.Render("(no agent types available)")
	if len(m.availableAgents) > 0 && m.agentForm.agentIdx < len(m.availableAgents) {
		a := m.availableAgents[m.agentForm.agentIdx]
		agentVal = a.Name + faint.Render("  "+a.Kind)
	}

	opts := m.agentNodeOptions()
	nodeVal := nodeLocal
	if m.agentForm.nodeIdx >= 0 && m.agentForm.nodeIdx < len(opts) {
		nodeVal = opts[m.agentForm.nodeIdx]
	}

	var b strings.Builder
	b.WriteString(titleStyle.Render("New agent"))
	b.WriteString("\n\n")
	b.WriteString(agentFormRow("Agent", agentVal, m.agentForm.cursor == agentFormFieldAgent))
	b.WriteString(agentFormRow("Node", nodeVal, m.agentForm.cursor == agentFormFieldNode))

	b.WriteString("\n")
	footer := "enter create · ↑↓ field · ←→ change · esc cancel"
	pad := (formInner - len(footer)) / centerDivisorForm
	if pad < 0 {
		pad = 0
	}
	b.WriteString(strings.Repeat(" ", pad))
	b.WriteString(faint.Render(footer))

	box := lipgloss.NewStyle().
		Width(formWidth).
		Padding(formPadY, formPadX).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("240"))
	return box.Render(b.String())
}

// agentFormRow renders one selector row; the focused row shows "‹ value ›" so
// left/right is discoverable.
func agentFormRow(label, value string, focused bool) string {
	if focused {
		return fmt.Sprintf("  %-11s %s\n", label, lipgloss.NewStyle().Reverse(true).Render(" "+value+" "))
	}
	return fmt.Sprintf("  %-11s %s\n", label, value)
}
