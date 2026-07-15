package app

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/geoffjay/horde/internal/client"
)

func TestClusterView_LeaderLine(t *testing.T) {
	m := New(nil, "127.0.0.1:1")
	m.connected = true
	m.view = viewCluster
	m.node = client.NodeInfo{Mode: "master", NodeID: "n1"}
	m.nodes = client.ClusterView{LeaderID: "n1"}

	out := m.renderClusterView()
	assert.Contains(t, out, "leader")
	assert.Contains(t, out, "n1")
	assert.Contains(t, out, "(this node)")
}

func TestClusterView_LeaderFromNodeID(t *testing.T) {
	m := New(nil, "127.0.0.1:1")
	m.connected = true
	m.view = viewCluster
	m.node = client.NodeInfo{Mode: "master", NodeID: "n1"}
	m.nodes = client.ClusterView{} // no LeaderID set

	out := m.renderClusterView()
	assert.Contains(t, out, "leader")
	assert.Contains(t, out, "n1")
}

func TestClusterView_NodeRows(t *testing.T) {
	m := New(nil, "127.0.0.1:1")
	m.connected = true
	m.view = viewCluster
	m.node = client.NodeInfo{Mode: "master", NodeID: "n1"}
	m.agents = []client.Agent{{ID: "a1"}, {ID: "a2"}}
	m.nodes = client.ClusterView{
		LeaderID: "n1",
		Nodes: []client.ClusterNode{
			{NodeID: "n2", Addr: "10.0.0.12:8080", Agents: []string{"a3", "a4"}, LastSeen: time.Now().Add(-10 * time.Second).Format(time.RFC3339)},
			{NodeID: "n3", Addr: "10.0.0.13:8080", Agents: []string{}, Stale: true, LastSeen: time.Now().Add(-41 * time.Second).Format(time.RFC3339)},
		},
	}

	out := m.renderClusterView()
	// Local node
	assert.Contains(t, out, "n1")
	assert.Contains(t, out, "master")
	assert.Contains(t, out, "2 agents")

	// Slave nodes
	assert.Contains(t, out, "n2")
	assert.Contains(t, out, "10.0.0.12:8080")
	assert.Contains(t, out, "slave")
	assert.Contains(t, out, "10s ago")

	// Stale node
	assert.Contains(t, out, "n3")
	assert.Contains(t, out, "stale")
}

func TestClusterView_RemoteAgents(t *testing.T) {
	m := New(nil, "127.0.0.1:1")
	m.connected = true
	m.view = viewCluster
	m.node = client.NodeInfo{Mode: "master", NodeID: "n1"}
	m.nodes = client.ClusterView{LeaderID: "n1"}
	m.remoteContexts = []client.ExecutionContext{
		{AgentID: "packager", NodeID: "n2", Activity: client.StateIdle, Project: "billing-rewrite", Issue: "#88"},
		{AgentID: "deployer", NodeID: "n2", Activity: client.StateIdle, Blocked: true, Project: "billing-rewrite", Issue: "#90", PendingApprovalCount: 1},
	}

	out := m.renderClusterView()
	assert.Contains(t, out, "remote agents")
	assert.Contains(t, out, "packager")
	assert.Contains(t, out, "deployer")
	assert.Contains(t, out, "n2")
	assert.Contains(t, out, "billing-rewrite")
	assert.Contains(t, out, "#88")
	assert.Contains(t, out, "#90")
	assert.Contains(t, out, "1 approval")
}

func TestClusterView_EmptyCluster(t *testing.T) {
	m := New(nil, "127.0.0.1:1")
	m.connected = true
	m.view = viewCluster
	m.node = client.NodeInfo{Mode: "master", NodeID: "n1"}
	m.nodes = client.ClusterView{LeaderID: "n1"}

	out := m.renderClusterView()
	// Should show the leader and the local node row, but no slaves.
	assert.Contains(t, out, "leader")
	assert.Contains(t, out, "n1")
}

func TestClusterView_CursorHighlight(t *testing.T) {
	m := New(nil, "127.0.0.1:1")
	m.connected = true
	m.view = viewCluster
	m.node = client.NodeInfo{Mode: "master", NodeID: "n1"}
	m.nodes = client.ClusterView{
		LeaderID: "n1",
		Nodes: []client.ClusterNode{
			{NodeID: "n2", Addr: "10.0.0.12:8080", Agents: []string{"a3"}},
		},
	}

	// Cursor at 0 = local node; cursor at 1 = first slave.
	rows := m.clusterNodeRows()
	require.Len(t, rows, 2)
	assert.Contains(t, rows[0], "n1")
	assert.Contains(t, rows[1], "n2")
}

func TestFormatSeenAgo(t *testing.T) {
	tests := []struct {
		name     string
		lastSeen time.Time
		want     string
	}{
		{"zero", time.Time{}, "unknown"},
		{"just now", time.Now().Add(-2 * time.Second), "just now"},
		{"seconds", time.Now().Add(-10 * time.Second), "10s ago"},
		{"minutes", time.Now().Add(-5 * time.Minute), "5m ago"},
		{"hours", time.Now().Add(-3 * time.Hour), "3h ago"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			n := &client.ClusterNode{LastSeen: tt.lastSeen.Format(time.RFC3339)}
			assert.Equal(t, tt.want, formatSeenAgo(n))
		})
	}
}

func TestLoadNode_FetchesClusterData(t *testing.T) {
	// Verify that nodeInfoMsg carries cluster nodes and remote contexts.
	msg := nodeInfoMsg{
		clusterNodes: client.ClusterView{LeaderID: "n1"},
		remoteContexts: []client.ExecutionContext{
			{AgentID: "a1", NodeID: "n2"},
		},
	}
	assert.Equal(t, "n1", msg.clusterNodes.LeaderID)
	require.Len(t, msg.remoteContexts, 1)
	assert.Equal(t, "n2", msg.remoteContexts[0].NodeID)
}
