//go:build integration

package server

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// This white-box integration test exercises AAP resume-token replication across
// a real 3-node raft cluster (gossip + raft transports, no HTTP): a token set on
// the leader replicates to the followers and survives a leader crash. It is
// package server (not server_test) so it can drive the unexported resume store
// directly, avoiding the AAP adapter machinery a full end-to-end turn needs.

// freeLoopbackAddr returns a likely-free 127.0.0.1:port (closed immediately).
func freeLoopbackAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	require.NoError(t, ln.Close())
	return addr
}

// startRaftServer starts an in-process failover node (gossip + raft, no HTTP
// router). It returns the server, its gossip address (a seed for followers), and
// a stop func that crashes just this node.
func startRaftServer(t *testing.T, nodeID string, bootstrap bool, seeds []string) (*Server, string, func()) {
	t.Helper()
	gossipAddr := freeLoopbackAddr(t)
	raftAddr := freeLoopbackAddr(t)
	mode := ModeSlave
	if bootstrap {
		mode = ModeMaster
	}
	s, err := New(Config{
		Mode:                mode,
		NodeID:              nodeID,
		SpawnDefaultAgent:   false,
		AdvertiseAddr:       freeLoopbackAddr(t),
		DiscoveryMechanism:  discoveryGossip,
		GossipBindAddr:      gossipAddr,
		GossipAdvertiseAddr: gossipAddr,
		GossipSeeds:         seeds,
		Failover:            FailoverRaft,
		RaftBindAddr:        raftAddr,
		RaftAdvertiseAddr:   raftAddr,
		RaftDir:             t.TempDir(),
	})
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	require.NoError(t, s.Start(ctx))

	var stopped bool
	stop := func() {
		if stopped {
			return
		}
		stopped = true
		cancel()
	}
	t.Cleanup(stop)
	return s, gossipAddr, stop
}

// awaitRaftLeader blocks until exactly one of the live servers holds leadership.
func awaitRaftLeader(t *testing.T, servers ...*Server) *Server {
	t.Helper()
	var leader *Server
	require.Eventually(t, func() bool {
		leader = nil
		count := 0
		for _, s := range servers {
			if s.IsLeader() {
				leader = s
				count++
			}
		}
		return count == 1
	}, 30*time.Second, 200*time.Millisecond, "expected exactly one raft leader")
	return leader
}

func TestRaftResume_ReplicatesAndSurvivesFailover(t *testing.T) {
	m, maddr, mstop := startRaftServer(t, "node-a", true, nil)
	f1, _, _ := startRaftServer(t, "node-b", false, []string{maddr})
	f2, _, _ := startRaftServer(t, "node-c", false, []string{maddr})

	leader := awaitRaftLeader(t, m, f1, f2)
	require.Equal(t, "node-a", leader.NodeID())

	// Wait until all three are voters so the survivors keep quorum after a crash.
	require.Eventually(t, func() bool {
		srv, err := m.raft.servers()
		return err == nil && len(srv) == 3
	}, 30*time.Second, 250*time.Millisecond, "all three nodes should become raft voters")

	// Set a resume token on the leader; it replicates through the raft log.
	m.resume.set("coder", "tok-42")
	require.Eventually(t, func() bool {
		return f1.resume.get("coder") == "tok-42" && f2.resume.get("coder") == "tok-42"
	}, 15*time.Second, 200*time.Millisecond, "the resume token should replicate to the followers")

	// Crash the leader; a survivor takes over with the replicated token.
	mstop()
	newLeader := awaitRaftLeader(t, f1, f2)
	assert.NotEqual(t, "node-a", newLeader.NodeID())
	assert.Equal(t, "tok-42", newLeader.resume.get("coder"),
		"the resume token should survive failover on the new leader")
}
