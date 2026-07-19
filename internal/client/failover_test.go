package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// deadAddr is a loopback address that reliably refuses connections, standing in
// for a crashed node.
const deadAddr = "127.0.0.1:1"

func TestClient_RotatesToLiveMemberOnTransportError(t *testing.T) {
	live := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(NodeInfo{NodeID: "node-b", Mode: "master"})
	}))
	defer live.Close()

	// The first member is unreachable (a crashed leader); the client must rotate
	// to the live member and succeed.
	c := NewCluster([]string{deadAddr, live.URL})
	n, err := c.Node(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "node-b", n.NodeID)
	assert.Equal(t, strings.TrimRight(live.URL, "/"), c.BaseURL(), "the client should now target the live member")
}

func TestClient_AllMembersDownReturnsError(t *testing.T) {
	c := NewCluster([]string{deadAddr, "127.0.0.1:2"})
	_, err := c.Node(context.Background())
	require.Error(t, err)
}

func TestClient_NoMembersErrors(t *testing.T) {
	c := NewCluster(nil)
	assert.Empty(t, c.BaseURL())
	_, err := c.Node(context.Background())
	require.Error(t, err)
}

func TestClient_LearnsMembersFromListNodes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(ClusterView{
			LeaderID: "node-a",
			Nodes: []ClusterNode{
				{NodeID: "node-b", Addr: "10.0.0.2:13420"},
				{NodeID: "node-c", Addr: "10.0.0.3:13420"},
			},
		})
	}))
	defer srv.Close()

	c := New(srv.URL)
	require.Len(t, c.Members(), 1)

	_, err := c.ListNodes(context.Background())
	require.NoError(t, err)

	members := c.Members()
	assert.Contains(t, members, "http://10.0.0.2:13420")
	assert.Contains(t, members, "http://10.0.0.3:13420")
	assert.Len(t, members, 3, "seed + two learned members")

	// A second call does not duplicate learned members.
	_, err = c.ListNodes(context.Background())
	require.NoError(t, err)
	assert.Len(t, c.Members(), 3)
}
