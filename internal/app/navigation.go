package app

import (
	tea "charm.land/bubbletea/v2"

	"github.com/geoffjay/horde/internal/client"
)

// maxDetailDepth bounds the transient detail drill (project → agent → invoke).
const maxDetailDepth = 2

// goCluster selects the Nodes group from the sidebar (the ctrl+l shortcut and
// the palette's Nodes command). It returns the command to run, if any.
func (m *Model) goCluster() tea.Cmd { return m.jumpToGroup(groupNodes) }

// pushDetail drills one level deeper within the detail pane, recording the
// current view so popDetail can return to it. The drill is bounded to
// maxDetailDepth; it is not a breadcrumb trail and carries no labels.
func (m *Model) pushDetail(v view) {
	m.actionErr = ""
	if len(m.detailBack) >= maxDetailDepth {
		m.detailBack = m.detailBack[len(m.detailBack)-maxDetailDepth+1:]
	}
	m.detailBack = append(m.detailBack, m.view)
	m.view = v
	m.cursor = 0
}

// popDetail returns one level up the detail drill, tearing down the stream for
// the view being left and re-opening the agent context stream when returning to
// the agent view. It returns (cmd, true) when a level was popped, or
// (nil, false) when the drill is empty (the detail pane is showing a top-level
// sidebar selection, so esc should return focus to the sidebar).
func (m *Model) popDetail() (tea.Cmd, bool) {
	if len(m.detailBack) == 0 {
		return nil, false
	}
	switch m.view {
	case viewAgent:
		m.unsubscribeAgentContext()
	case viewInvoke:
		m.unsubscribeInvoke()
	}
	last := m.detailBack[len(m.detailBack)-1]
	m.detailBack = m.detailBack[:len(m.detailBack)-1]
	m.view = last
	m.cursor = 0
	m.approvalCursor = 0
	m.actionErr = ""

	switch last {
	case viewProjectDetail, viewTeamDetail:
		// Returning to a team roster: selection is cursor-driven again.
		m.selectedAgentID = ""
	case viewAgent:
		if a, ok := m.selectedAgent(); ok {
			return m.subscribeAgentContext(a.ID), true
		}
	}
	return nil, true
}

// detailEnter handles enter within the detail pane: it drills one level along
// the transient detail path (project/team roster → agent → invoke; agents list
// → invoke). It replaces the old breadcrumb drill-down.
func (m *Model) detailEnter() (tea.Model, tea.Cmd) {
	switch m.view {
	case viewProjectDetail, viewTeamDetail:
		if a, ok := m.selectedAgent(); ok {
			m.pushDetail(viewAgent)
			m.selectedAgentID = a.ID
			return m, m.subscribeAgentContext(a.ID) //nolint:gocritic // evalOrder: returning the cmd is the intended pattern
		}
	case viewAgent:
		if a, ok := m.selectedAgent(); ok {
			m.pushDetail(viewInvoke)
			m.selectedAgentID = a.ID
			m.resetInvokeState()
			return m, nil
		}
	case viewAgents:
		if a, ok := m.selectedAgent(); ok {
			m.pushDetail(viewInvoke)
			m.selectedAgentID = a.ID
			m.resetInvokeState()
			return m, nil
		}
	}
	return m, nil
}

// selectedProjectIndex returns the index into m.projects of the project open in
// the current view, or -1 if not found. In the projects/teams overview it is the
// cursor position; in drill-down views (projectDetail, teamDetail, agent,
// invoke) it is the project selected from the sidebar, tracked by
// selectedProjectID.
func (m *Model) selectedProjectIndex() int {
	if m.selectedProjectID != "" {
		for i := range m.projects {
			if m.projects[i].ID == m.selectedProjectID {
				return i
			}
		}
		return -1
	}
	if m.cursor >= 0 && m.cursor < len(m.projects) {
		return m.cursor
	}
	return -1
}

// selectedAgent returns the agent selected in the current context, if any. A
// pinned agent (selectedAgentID, set when drilling into the agent/invoke views)
// is resolved by id from the node's agents; otherwise the cursor indexes into
// the current view's visible agent list.
func (m *Model) selectedAgent() (client.Agent, bool) {
	if m.selectedAgentID != "" {
		for _, a := range m.agents {
			if a.ID == m.selectedAgentID {
				return a, true
			}
		}
		// A team agent drilled in from a project roster is synthesized, not a
		// standalone node agent, so fall back to the current view's agents.
		for _, a := range m.visibleAgents() {
			if a.ID == m.selectedAgentID {
				return a, true
			}
		}
	}
	agents := m.visibleAgents()
	if m.cursor >= 0 && m.cursor < len(agents) {
		return agents[m.cursor], true
	}
	return client.Agent{}, false
}

// visibleAgents returns the agents relevant to the current view. In the project/
// team roster and agent views these are the selected project's team agents
// (synthesized into client.Agent values so detailEnter and selectedAgent can use
// a uniform type); in other views they are the node's running agents.
func (m *Model) visibleAgents() []client.Agent {
	switch m.view {
	case viewProjectDetail, viewTeamDetail, viewAgent, viewInvoke:
		i := m.selectedProjectIndex()
		if i < 0 {
			return nil
		}
		team := m.projects[i].Team.Agents
		agents := make([]client.Agent, len(team))
		for j, ta := range team {
			agents[j] = client.Agent{ID: ta.AgentID, Name: ta.Name, Status: "running"}
		}
		return agents
	}
	return m.agents
}
