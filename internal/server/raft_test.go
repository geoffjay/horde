package server

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeRaftLeader is a raftLeaderSource double.
type fakeRaftLeader struct{ id string }

func (f *fakeRaftLeader) leaderID() string { return f.id }

// fakeAPIResolver is an apiAddrResolver double mapping node id → HTTP address.
type fakeAPIResolver struct{ addrs map[string]string }

func (f *fakeAPIResolver) apiAddrForNode(nodeID string) (string, bool) {
	a, ok := f.addrs[nodeID]
	return a, ok
}

func TestRaftDiscoverer_ResolvesLeaderAddr(t *testing.T) {
	d := &raftDiscoverer{
		raft:   &fakeRaftLeader{id: "node-b"},
		gossip: &fakeAPIResolver{addrs: map[string]string{"node-b": "10.0.0.2:13420"}},
	}
	addr, err := d.Leader(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "10.0.0.2:13420", addr)
	assert.Equal(t, "raft", d.Describe())
}

func TestRaftDiscoverer_NoLeaderYet(t *testing.T) {
	d := &raftDiscoverer{
		raft:   &fakeRaftLeader{id: ""},
		gossip: &fakeAPIResolver{addrs: map[string]string{}},
	}
	_, err := d.Leader(context.Background())
	require.Error(t, err)
	assert.True(t, errors.Is(err, errNoRaftLeader))
}

func TestRaftDiscoverer_LeaderNotInRingYet(t *testing.T) {
	// The leader is elected but its HTTP address has not propagated in gossip.
	d := &raftDiscoverer{
		raft:   &fakeRaftLeader{id: "node-c"},
		gossip: &fakeAPIResolver{addrs: map[string]string{"node-b": "10.0.0.2:13420"}},
	}
	_, err := d.Leader(context.Background())
	require.Error(t, err)
}
