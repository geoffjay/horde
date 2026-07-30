//go:build integration

package server

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGossipNode_WorkerDiscoversCoordinator stands up two real memberlist nodes on
// the loopback interface (ephemeral ports) and confirms a worker that joins via
// the coordinator's address discovers the coordinator's advertised HTTP address through
// the gossiped membership.
func TestGossipNode_WorkerDiscoversCoordinator(t *testing.T) {
	coordinator, err := newGossipNode(gossipConfig{
		NodeID:   "coordinator",
		Role:     roleCoordinator,
		APIAddr:  "coordinator:13420",
		BindAddr: "127.0.0.1:0",
	})
	require.NoError(t, err)
	defer coordinator.shutdown()

	// The coordinator resolves itself as leader immediately.
	self, err := coordinator.leaderAPIAddr()
	require.NoError(t, err)
	assert.Equal(t, "coordinator:13420", self)

	// Seed the worker with the coordinator's actual bound gossip address.
	seed := coordinator.ml.LocalNode().Address()
	worker, err := newGossipNode(gossipConfig{
		NodeID:   "worker-1",
		Role:     roleWorker,
		APIAddr:  "worker1:13420",
		BindAddr: "127.0.0.1:0",
		Seeds:    []string{seed},
	})
	require.NoError(t, err)
	defer worker.shutdown()

	// The worker converges on the coordinator's HTTP address via gossip.
	var addr string
	require.Eventually(t, func() bool {
		a, e := worker.leaderAPIAddr()
		if e == nil {
			addr = a
			return true
		}
		return false
	}, 5*time.Second, 50*time.Millisecond)
	assert.Equal(t, "coordinator:13420", addr)
}

// TestGossipNode_EncryptedConverges confirms two nodes sharing a SecretKey
// still form a ring and discover the coordinator (encryption is transparent to
// discovery).
func TestGossipNode_EncryptedConverges(t *testing.T) {
	key := make([]byte, 16) // AES-128; all-zero is fine for the test
	coordinator, err := newGossipNode(gossipConfig{
		NodeID:    "coordinator",
		Role:      roleCoordinator,
		APIAddr:   "coordinator:13420",
		BindAddr:  "127.0.0.1:0",
		SecretKey: key,
	})
	require.NoError(t, err)
	defer coordinator.shutdown()

	worker, err := newGossipNode(gossipConfig{
		NodeID:    "worker-1",
		Role:      roleWorker,
		APIAddr:   "worker1:13420",
		BindAddr:  "127.0.0.1:0",
		Seeds:     []string{coordinator.ml.LocalNode().Address()},
		SecretKey: key,
	})
	require.NoError(t, err)
	defer worker.shutdown()

	require.Eventually(t, func() bool {
		_, e := worker.leaderAPIAddr()
		return e == nil
	}, 5*time.Second, 50*time.Millisecond)
}
