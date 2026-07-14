package app

import (
	tea "charm.land/bubbletea/v2"

	"github.com/geoffjay/horde/internal/client"
)

// pushView navigates to a new view, recording the current one on the
// breadcrumb stack so esc returns to it. The id and label are used to
// restore the cursor when returning (e.g. highlighting the project that
// was opened).
func (m *Model) pushView(v view, id, _ string) {
	m.crumbs = append(m.crumbs, breadcrumbEntry{view: m.view, id: m.crumbID(), label: m.crumbLabel()})
	m.view = v
	switch v {
	case viewProjectDetail:
		m.setProjectCursor(id)
	case viewAgent, viewInvoke:
		m.cursor = 0
	}
}

// popView returns to the previous view in the breadcrumb stack. If the
// stack is empty it stays on the current view (top-level screens have
// nowhere to go back to). The cursor is restored to the item that was
// selected when we drilled in, so returning to a list keeps the
// previously-opened row highlighted.
func (m *Model) popView() {
	if len(m.crumbs) == 0 {
		return
	}
	last := m.crumbs[len(m.crumbs)-1]
	m.crumbs = m.crumbs[:len(m.crumbs)-1]
	m.view = last.view
	if last.id != "" {
		switch last.view {
		case viewProjects:
			m.setProjectCursor(last.id)
		default:
			m.cursor = 0
		}
	} else {
		m.cursor = 0
	}
}

// crumbID returns the id of the current view's selection (the project or
// agent id), used to restore the cursor when returning to this view.
// For top-level screens (projects, cluster) it returns the selected
// project id so popping back to the projects list highlights the
// previously-opened project.
func (m *Model) crumbID() string {
	switch m.view {
	case viewProjects:
		if i := m.selectedProjectIndex(); i >= 0 {
			return m.projects[i].ID
		}
	case viewProjectDetail:
		if a, ok := m.selectedAgent(); ok {
			return a.ID
		}
	case viewAgent, viewInvoke:
		if a, ok := m.selectedAgent(); ok {
			return a.ID
		}
	}
	return ""
}

// crumbLabel returns the display label for the current view's selection,
// used when pushing it onto the breadcrumb stack.
func (m *Model) crumbLabel() string {
	switch m.view {
	case viewProjects:
		return "projects"
	case viewCluster:
		return "cluster"
	case viewProjectDetail:
		if i := m.selectedProjectIndex(); i >= 0 && i < len(m.projects) {
			return m.projects[i].Name
		}
	case viewAgent:
		if a, ok := m.selectedAgent(); ok {
			return a.Name
		}
	case viewInvoke:
		if a, ok := m.selectedAgent(); ok {
			return a.Name + " › invoke"
		}
	}
	return ""
}

// goHome resets to the projects home view, clearing the breadcrumb stack.
func (m *Model) goHome() {
	m.view = viewProjects
	m.crumbs = nil
	m.cursor = 0
}

// goCluster navigates to the cluster view, clearing the breadcrumb stack.
func (m *Model) goCluster() {
	m.view = viewCluster
	m.crumbs = nil
	m.cursor = 0
}

// selectedProjectIndex returns the index into m.projects that the cursor
// points at, or -1 if out of range.
func (m *Model) selectedProjectIndex() int {
	if m.cursor >= 0 && m.cursor < len(m.projects) {
		return m.cursor
	}
	return -1
}

// setProjectCursor sets the cursor to the project with the given id, so
// returning to the projects list keeps the previously opened project
// highlighted.
func (m *Model) setProjectCursor(id string) {
	for i, p := range m.projects {
		if p.ID == id {
			m.cursor = i
			return
		}
	}
	m.cursor = 0
}

// selectedAgent returns the agent selected in the current context, if any.
// In the project detail view, agents come from the project's team; in the
// agent view, from the agent list.
func (m *Model) selectedAgent() (client.Agent, bool) {
	agents := m.visibleAgents()
	if m.cursor >= 0 && m.cursor < len(agents) {
		return agents[m.cursor], true
	}
	return client.Agent{}, false
}

// visibleAgents returns the agents relevant to the current view: the
// project's team agents in project detail, or all agents in other views.
func (m *Model) visibleAgents() []client.Agent {
	return m.agents
}

// drillIn handles enter on the current view: projects → project detail,
// project detail → agent, agent → invoke, cluster → (node, future).
func (m *Model) drillIn() (tea.Model, tea.Cmd) {
	switch m.view {
	case viewProjects:
		i := m.selectedProjectIndex()
		if i < 0 {
			return m, nil
		}
		p := m.projects[i]
		m.pushView(viewProjectDetail, p.ID, p.Name)
		return m, nil
	case viewProjectDetail:
		if a, ok := m.selectedAgent(); ok {
			m.pushView(viewAgent, a.ID, a.Name)
			return m, nil
		}
	case viewAgent:
		if a, ok := m.selectedAgent(); ok {
			m.pushView(viewInvoke, a.ID, a.Name)
			return m, nil
		}
	}
	return m, nil
}
