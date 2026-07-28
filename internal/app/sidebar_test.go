package app

import (
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/geoffjay/horde/internal/client"
)

func TestSidebarRows_CollapsedOrder(t *testing.T) {
	m := newTestModel("127.0.0.1:1")
	rows := m.sidebarRows()

	// All groups collapsed: five group headers followed by two feed leaves.
	require.Len(t, rows, 7)
	want := []groupID{groupNodes, groupUsers, groupAgents, groupTeams, groupProjects, leafActivity, leafLogs}
	for i, g := range want {
		assert.Equal(t, g, rows[i].group, "row %d group", i)
	}
	assert.Equal(t, rowGroup, rows[0].kind)
	assert.Equal(t, rowLeaf, rows[5].kind, "Activity is a leaf")
	assert.Equal(t, rowLeaf, rows[6].kind, "Logs is a leaf")
}

func TestSidebarRows_ExpandSplicesChildren(t *testing.T) {
	m := newTestModel("127.0.0.1:1")
	m.projects = []client.Project{
		{ID: "p1", Name: "auth"},
		{ID: "p2", Name: "billing"},
	}
	m.sidebar.expanded[groupProjects] = true

	rows := m.sidebarRows()
	// 5 headers + 2 leaves + 2 project children.
	require.Len(t, rows, 9)

	// The Projects header is followed immediately by its two children.
	idx := m.groupRowIndex(groupProjects)
	assert.Equal(t, rowChild, rows[idx+1].kind)
	assert.Equal(t, "p1", rows[idx+1].id)
	assert.Equal(t, "p2", rows[idx+2].id)
}

func TestSidebarRows_TeamsDerivedFromProjects(t *testing.T) {
	m := newTestModel("127.0.0.1:1")
	m.projects = []client.Project{
		{ID: "p1", Name: "auth"},
		{ID: "p2", Name: "billing"},
	}
	m.sidebar.expanded[groupTeams] = true

	children := m.groupChildren(groupTeams)
	require.Len(t, children, 2, "one team entry per project")
	assert.Equal(t, "auth", children[0].label)
	assert.Equal(t, "p2", children[1].id)
}

func TestSidebarRows_UsersHasNoChildren(t *testing.T) {
	m := newTestModel("127.0.0.1:1")
	m.sidebar.expanded[groupUsers] = true
	assert.Empty(t, m.groupChildren(groupUsers))
}

func TestSidebarRows_NodesIncludesLocalAndSlaves(t *testing.T) {
	m := newTestModel("127.0.0.1:1")
	m.node.NodeID = "n1"
	m.nodes = client.ClusterView{Nodes: []client.ClusterNode{
		{NodeID: "n1"}, // duplicate of local — must be deduped
		{NodeID: "n2"},
	}}

	children := m.groupChildren(groupNodes)
	require.Len(t, children, 2)
	assert.Equal(t, "n1", children[0].id, "local node first")
	assert.Equal(t, "n2", children[1].id)
}

func TestMoveSidebarCursor_Clamps(t *testing.T) {
	m := newTestModel("127.0.0.1:1")
	n := m.sidebarLen()

	m.sidebar.cursor = 0
	m.moveSidebarCursor(-1)
	assert.Equal(t, 0, m.sidebar.cursor, "clamps at the top")

	m.moveSidebarCursor(n + 5)
	assert.Equal(t, n-1, m.sidebar.cursor, "clamps at the bottom")
}

func TestApplySelection_ChildMappings(t *testing.T) {
	m := newTestModel("127.0.0.1:1")
	m.projects = []client.Project{{ID: "p1", Name: "auth"}}
	m.agents = []client.Agent{{ID: "a1", Name: "greeter"}}

	m.applySelection(sidebarRow{kind: rowChild, group: groupProjects, id: "p1"})
	assert.Equal(t, viewProjectDetail, m.view)
	assert.Equal(t, "p1", m.selectedProjectID)

	m.applySelection(sidebarRow{kind: rowChild, group: groupTeams, id: "p1"})
	assert.Equal(t, viewTeamDetail, m.view)
	assert.Equal(t, "p1", m.selectedProjectID)

	m.applySelection(sidebarRow{kind: rowChild, group: groupNodes, id: "n1"})
	assert.Equal(t, viewCluster, m.view)
}

func TestApplySelection_GroupAndLeafOverviews(t *testing.T) {
	m := newTestModel("127.0.0.1:1")

	cases := []struct {
		row  sidebarRow
		want view
	}{
		{sidebarRow{kind: rowGroup, group: groupNodes}, viewCluster},
		{sidebarRow{kind: rowGroup, group: groupUsers}, viewUsers},
		{sidebarRow{kind: rowGroup, group: groupAgents}, viewAgents},
		{sidebarRow{kind: rowGroup, group: groupTeams}, viewTeams},
		{sidebarRow{kind: rowGroup, group: groupProjects}, viewProjects},
		{sidebarRow{kind: rowLeaf, group: leafLogs}, viewLogs},
	}
	for _, c := range cases {
		m.applySelection(c.row)
		assert.Equal(t, c.want, m.view, "group %d", c.row.group)
	}
}

func TestApplySelection_ResetsDetailState(t *testing.T) {
	m := newTestModel("127.0.0.1:1")
	m.detailBack = []view{viewAgent}
	m.cursor = 3
	m.approvalCursor = 2
	m.actionErr = "boom"
	m.selectedAgentID = "a9"

	m.applySelection(sidebarRow{kind: rowGroup, group: groupProjects})
	assert.Empty(t, m.detailBack)
	assert.Equal(t, 0, m.cursor)
	assert.Equal(t, 0, m.approvalCursor)
	assert.Empty(t, m.actionErr)
	assert.Empty(t, m.selectedAgentID)
}

func TestSidebarKey_EnterOnGroupTogglesExpand(t *testing.T) {
	m := newTestModel("127.0.0.1:1")
	m.connected = true
	// Cursor starts on the Projects group header.
	require.Equal(t, groupProjects, mustRow(t, m).group)

	m.Update(namedKey(tea.KeyEnter))
	assert.True(t, m.sidebar.expanded[groupProjects])
	assert.Equal(t, focusSidebar, m.focus, "expanding a group keeps focus on the sidebar")

	m.Update(namedKey(tea.KeyEnter))
	assert.False(t, m.sidebar.expanded[groupProjects], "enter again collapses")
}

func TestSidebarKey_TabAndEscSwitchFocus(t *testing.T) {
	m, stub := connectedNavModel(t)
	defer stub.Close()

	// tab moves focus to the detail pane.
	m.Update(tabKey())
	assert.Equal(t, focusDetail, m.focus)

	// esc with an empty drill returns focus to the sidebar.
	m.Update(escKey())
	assert.Equal(t, focusSidebar, m.focus)
}

func TestJumpToChild_SelectsAndFocusesDetail(t *testing.T) {
	m, stub := connectedNavModel(t)
	defer stub.Close()

	m.jumpToChild(groupProjects, "p2")
	assert.Equal(t, viewProjectDetail, m.view)
	assert.Equal(t, "p2", m.selectedProjectID)
	assert.Equal(t, focusDetail, m.focus)
	assert.True(t, m.sidebar.expanded[groupProjects])
	// The sidebar cursor now points at the selected child.
	row := mustRow(t, m)
	assert.Equal(t, rowChild, row.kind)
	assert.Equal(t, "p2", row.id)
}

func TestRenderSidebar_ShowsGroupsAndCarets(t *testing.T) {
	m, stub := connectedNavModel(t)
	defer stub.Close()

	out := m.renderSidebar()
	for _, label := range []string{"Nodes", "Users", "Agents", "Teams", "Projects", "Activity", "Logs"} {
		assert.Contains(t, out, label)
	}
	assert.Contains(t, out, "▸", "collapsed groups show a right caret")

	m.sidebar.expanded[groupProjects] = true
	out = m.renderSidebar()
	assert.Contains(t, out, "▾", "expanded groups show a down caret")
	assert.Contains(t, out, "auth-service", "expanded Projects lists its children")
}

func TestRenderUsersView_ShowsPlaceholder(t *testing.T) {
	m := newTestModel("127.0.0.1:1")
	// With no node fetched, auth is disabled by default; the view explains it.
	assert.Contains(t, m.renderUsersView(), "auth is disabled")
}

// tabKey constructs a KeyPressMsg for the tab key.
func tabKey() tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: tea.KeyTab}
}
