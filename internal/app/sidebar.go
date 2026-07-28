package app

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// sidebarWidth is the fixed column width of the left navigation pane.
const sidebarWidth = 22

// labelAgents is the shared "Agents" label used by the sidebar group, the
// palette command, and the new-project form field.
const labelAgents = "Agents"

// groupID identifies a top-level sidebar entry: one of the five expandable
// groups or one of the two standalone feed leaves.
type groupID int

const (
	groupNodes groupID = iota
	groupUsers
	groupAgents
	groupTeams
	groupProjects
	leafActivity
	leafLogs
)

// topLevelOrder is the fixed top-to-bottom order of sidebar entries: the five
// expandable groups (Nodes, Users, Agents, Teams, Projects) followed by the two
// standalone feed leaves (Activity, Logs).
var topLevelOrder = []groupID{
	groupNodes, groupUsers, groupAgents, groupTeams, groupProjects,
	leafActivity, leafLogs,
}

// label returns the display name for a group or leaf.
func (g groupID) label() string {
	switch g {
	case groupNodes:
		return "Nodes"
	case groupUsers:
		return "Users"
	case groupAgents:
		return labelAgents
	case groupTeams:
		return "Teams"
	case groupProjects:
		return "Projects"
	case leafActivity:
		return "Activity"
	case leafLogs:
		return "Logs"
	}
	return ""
}

// isLeaf reports whether g is a standalone feed entry (Activity/Logs) rather
// than an expandable group.
func (g groupID) isLeaf() bool { return g == leafActivity || g == leafLogs }

// sidebarKind distinguishes a group header row, a child (entity) row, and a
// standalone leaf row.
type sidebarKind int

const (
	rowGroup sidebarKind = iota
	rowChild
	rowLeaf
)

// sidebarRow is one rendered/navigable line in the sidebar. For rowChild, id is
// the entity id (project/agent/node) used to drive the detail selection.
type sidebarRow struct {
	kind  sidebarKind
	group groupID
	id    string
	label string
}

// sidebar holds the navigation state: which top-level groups are expanded and
// the cursor position within the flattened row list.
type sidebar struct {
	cursor   int
	expanded map[groupID]bool
}

// focus identifies which pane keyboard input drives.
type focus int

const (
	focusSidebar focus = iota
	focusDetail
)

// sidebarRows builds the flattened, cursor-navigable row list from the fixed
// top-level order, splicing the children of each expanded group in below its
// header. It is rebuilt from live model data on every render/navigation.
func (m *Model) sidebarRows() []sidebarRow {
	var rows []sidebarRow
	for _, g := range topLevelOrder {
		if g.isLeaf() {
			rows = append(rows, sidebarRow{kind: rowLeaf, group: g, label: g.label()})
			continue
		}
		rows = append(rows, sidebarRow{kind: rowGroup, group: g, label: g.label()})
		if m.sidebar.expanded[g] {
			rows = append(rows, m.groupChildren(g)...)
		}
	}
	return rows
}

// sidebarLen is the number of rows currently in the sidebar (used to clamp the
// sidebar cursor).
func (m *Model) sidebarLen() int { return len(m.sidebarRows()) }

// groupChildren returns the child rows for an expandable group, sourced from
// live model data. Nodes lists this node then registered slaves; Agents the
// node's running agents; Teams one entry per project (derived — there is no
// team API); Projects the projects. Users has no children (per-user data does
// not exist yet).
func (m *Model) groupChildren(g groupID) []sidebarRow {
	var rows []sidebarRow
	switch g {
	case groupNodes:
		local := m.node.NodeID
		if local == "" {
			local = "local"
		}
		rows = append(rows, sidebarRow{kind: rowChild, group: g, id: m.node.NodeID, label: local})
		for _, n := range m.nodes.Nodes {
			if n.NodeID == m.node.NodeID {
				continue
			}
			rows = append(rows, sidebarRow{kind: rowChild, group: g, id: n.NodeID, label: n.NodeID})
		}
	case groupAgents:
		for _, a := range m.agents {
			rows = append(rows, sidebarRow{kind: rowChild, group: g, id: a.ID, label: a.Name})
		}
	case groupTeams:
		for i := range m.projects {
			rows = append(rows, sidebarRow{kind: rowChild, group: g, id: m.projects[i].ID, label: m.projects[i].Name})
		}
	case groupProjects:
		for i := range m.projects {
			rows = append(rows, sidebarRow{kind: rowChild, group: g, id: m.projects[i].ID, label: m.projects[i].Name})
		}
	case groupUsers:
		// No children — per-user accounts do not exist yet.
	}
	return rows
}

// currentSidebarRow returns the row under the sidebar cursor, if any.
func (m *Model) currentSidebarRow() (sidebarRow, bool) {
	rows := m.sidebarRows()
	if m.sidebar.cursor < 0 || m.sidebar.cursor >= len(rows) {
		return sidebarRow{}, false
	}
	return rows[m.sidebar.cursor], true
}

// groupRowIndex returns the flattened index of a group/leaf header row, or 0 if
// not found.
func (m *Model) groupRowIndex(g groupID) int {
	for i, r := range m.sidebarRows() {
		if (r.kind == rowGroup || r.kind == rowLeaf) && r.group == g {
			return i
		}
	}
	return 0
}

// moveSidebarCursor moves the sidebar cursor by delta, clamped to the row list.
func (m *Model) moveSidebarCursor(delta int) {
	n := m.sidebarLen()
	if n == 0 {
		m.sidebar.cursor = 0
		return
	}
	m.sidebar.cursor += delta
	if m.sidebar.cursor < 0 {
		m.sidebar.cursor = 0
	}
	if m.sidebar.cursor >= n {
		m.sidebar.cursor = n - 1
	}
}

// handleSidebarKey handles key presses while the sidebar has focus: up/down move
// the cursor; enter toggles a group (and shows its overview) or selects a
// child/leaf and hands focus to the detail pane; right/left expand/collapse;
// tab jumps focus to the detail pane.
func (m *Model) handleSidebarKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case keyUp, "k":
		m.moveSidebarCursor(-1)
		return m, nil
	case keyDown, "j":
		m.moveSidebarCursor(1)
		return m, nil
	case keyEnter:
		return m.sidebarActivate()
	case keyRight, "l":
		return m.sidebarExpandOrEnter()
	case keyLeft, "h":
		m.sidebarCollapse()
		return m, nil
	case keyTab:
		m.focus = focusDetail
		return m, nil
	}
	return m, nil
}

// sidebarActivate handles enter on the current sidebar row: toggling an
// expandable group (and pointing the detail pane at its overview) or selecting a
// child/leaf and moving focus to the detail pane.
func (m *Model) sidebarActivate() (tea.Model, tea.Cmd) {
	row, ok := m.currentSidebarRow()
	if !ok {
		return m, nil
	}
	if row.kind == rowGroup {
		m.sidebar.expanded[row.group] = !m.sidebar.expanded[row.group]
		return m, m.applySelection(row) //nolint:gocritic // evalOrder: returning the cmd is the intended pattern
	}
	cmd := m.applySelection(row)
	m.focus = focusDetail
	return m, cmd
}

// sidebarExpandOrEnter handles right/l: expanding a collapsed group (never
// collapsing), or selecting a child/leaf and moving focus to the detail pane.
func (m *Model) sidebarExpandOrEnter() (tea.Model, tea.Cmd) {
	row, ok := m.currentSidebarRow()
	if !ok {
		return m, nil
	}
	if row.kind == rowGroup {
		m.sidebar.expanded[row.group] = true
		return m, m.applySelection(row) //nolint:gocritic // evalOrder: returning the cmd is the intended pattern
	}
	cmd := m.applySelection(row)
	m.focus = focusDetail
	return m, cmd
}

// sidebarCollapse handles left/h: collapsing the current group, or collapsing a
// child's parent group and moving the cursor up to its header.
func (m *Model) sidebarCollapse() {
	row, ok := m.currentSidebarRow()
	if !ok {
		return
	}
	switch row.kind {
	case rowGroup:
		m.sidebar.expanded[row.group] = false
	case rowChild:
		m.sidebar.expanded[row.group] = false
		m.sidebar.cursor = m.groupRowIndex(row.group)
	}
}

// applySelection points the detail pane at the entity or feed named by row. It
// is the single place the sidebar sets m.view: it first tears down any active
// stream and resets transient detail state (drill, cursors, action error), then
// maps the row to a view (and, for children, the selected id and any stream).
func (m *Model) applySelection(row sidebarRow) tea.Cmd {
	m.unsubscribeAgentContext()
	m.unsubscribeInvoke()
	m.unsubscribeEvents()
	m.detailBack = nil
	m.cursor = 0
	m.approvalCursor = 0
	m.actionErr = ""
	m.selectedProjectID = ""
	m.selectedAgentID = ""

	switch row.kind {
	case rowGroup:
		return m.applyGroupOverview(row.group)
	case rowLeaf:
		return m.applyLeaf(row.group)
	case rowChild:
		return m.applyChild(row)
	}
	return nil
}

// applyGroupOverview points the detail pane at a group's overview list.
func (m *Model) applyGroupOverview(g groupID) tea.Cmd {
	switch g {
	case groupNodes:
		m.view = viewCluster
	case groupUsers:
		m.view = viewUsers
	case groupAgents:
		m.view = viewAgents
	case groupTeams:
		m.view = viewTeams
	case groupProjects:
		m.view = viewProjects
	}
	return nil
}

// applyLeaf points the detail pane at a standalone feed (Activity or Logs),
// opening the activity stream where needed.
func (m *Model) applyLeaf(g groupID) tea.Cmd {
	switch g {
	case leafActivity:
		m.view = viewEvents
		return m.subscribeEvents()
	case leafLogs:
		m.view = viewLogs
		m.logScroll = 0
	}
	return nil
}

// applyChild points the detail pane at a specific entity, opening the agent
// context stream when an agent is selected.
func (m *Model) applyChild(row sidebarRow) tea.Cmd {
	switch row.group {
	case groupNodes:
		m.view = viewCluster
		m.setNodeCursor(row.id)
	case groupUsers:
		m.view = viewUsers
	case groupAgents:
		m.view = viewAgent
		m.selectedAgentID = row.id
		return m.subscribeAgentContext(row.id)
	case groupTeams:
		m.view = viewTeamDetail
		m.selectedProjectID = row.id
	case groupProjects:
		m.view = viewProjectDetail
		m.selectedProjectID = row.id
	}
	return nil
}

// setNodeCursor sets the detail cursor to the cluster row for the given node id
// (this node is row 0, registered slaves follow).
func (m *Model) setNodeCursor(id string) {
	if id == m.node.NodeID {
		m.cursor = 0
		return
	}
	for i, n := range m.nodes.Nodes {
		if n.NodeID == id {
			m.cursor = i + 1
			return
		}
	}
	m.cursor = 0
}

// jumpToGroup selects a top-level group or leaf from elsewhere (the palette
// nav commands and the go* shortcuts): it expands a group, moves the sidebar
// cursor to it, applies the selection, and leaves focus on the sidebar.
func (m *Model) jumpToGroup(g groupID) tea.Cmd {
	if !g.isLeaf() {
		m.sidebar.expanded[g] = true
	}
	m.sidebar.cursor = m.groupRowIndex(g)
	m.focus = focusSidebar
	row, ok := m.currentSidebarRow()
	if !ok {
		return nil
	}
	return m.applySelection(row)
}

// jumpToChild selects a specific child (e.g. the Switch Project picker): it
// expands the parent group, moves the sidebar cursor to the child, applies the
// selection, and moves focus to the detail pane. Falls back to the group
// overview when the child is not found.
func (m *Model) jumpToChild(g groupID, id string) tea.Cmd {
	m.sidebar.expanded[g] = true
	for i, r := range m.sidebarRows() {
		if r.kind == rowChild && r.group == g && r.id == id {
			m.sidebar.cursor = i
			cmd := m.applySelection(r)
			m.focus = focusDetail
			return cmd
		}
	}
	return m.jumpToGroup(g)
}

// renderSidebar renders the left navigation column: each top-level group and
// feed leaf, with the children of expanded groups indented below.
func (m *Model) renderSidebar() string {
	rows := m.sidebarRows()
	var b strings.Builder
	for i, r := range rows {
		b.WriteString(m.renderSidebarRow(r, i == m.sidebar.cursor) + "\n")
	}
	return b.String()
}

// renderSidebarRow renders one sidebar row. The row under the cursor is shown
// with the full highlight (selStyle) while the sidebar has focus, and in bold
// otherwise so the current location stays visible when focus is in the detail
// pane. All styling goes through m.paint so it dims uniformly under an overlay.
func (m *Model) renderSidebarRow(r sidebarRow, atCursor bool) string {
	var text string
	switch r.kind {
	case rowGroup:
		caret := "▸"
		if m.sidebar.expanded[r.group] {
			caret = "▾"
		}
		text = caret + " " + r.label
	case rowLeaf:
		text = "· " + r.label
	case rowChild:
		text = "   " + r.label
	}

	if atCursor && m.focus == focusSidebar {
		return m.paint(selStyle().Render, text)
	}
	if atCursor || r.kind == rowGroup {
		return m.paint(lipgloss.NewStyle().Bold(true).Render, text)
	}
	return text
}
