//go:build integration

package server

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGossipNode_SlaveDiscoversMaster stands up two real memberlist nodes on
// the loopback interface (ephemeral ports) and confirms a slave that joins via
// the master's address discovers the master's advertised HTTP address through
// the gossiped membership.
func TestGossipNode_SlaveDiscoversMaster(t *testing.T) {
	master, err := newGossipNode(gossipConfig{
		NodeID:   "master",
		Role:     roleMaster,
		APIAddr:  "master:13420",
		BindAddr: "127.0.0.1:0",
	})
	require.NoError(t, err)
	defer master.shutdown()

	// The master resolves itself as leader immediately.
	self, err := master.leaderAPIAddr()
	require.NoError(t, err)
	assert.Equal(t, "master:13420", self)

	// Seed the slave with the master's actual bound gossip address.
	seed := master.ml.LocalNode().Address()
	slave, err := newGossipNode(gossipConfig{
		NodeID:   "slave-1",
		Role:     roleSlave,
		APIAddr:  "slave1:13420",
		BindAddr: "127.0.0.1:0",
		Seeds:    []string{seed},
	})
	require.NoError(t, err)
	defer slave.shutdown()

	// The slave converges on the master's HTTP address via gossip.
	var addr string
	require.Eventually(t, func() bool {
		a, e := slave.leaderAPIAddr()
		if e == nil {
			addr = a
			return true
		}
		return false
	}, 5*time.Second, 50*time.Millisecond)
	assert.Equal(t, "master:13420", addr)
}

// TestGossipNode_EncryptedConverges confirms two nodes sharing a SecretKey
// still form a ring and discover the master (encryption is transparent to
// discovery).
func TestGossipNode_EncryptedConverges(t *testing.T) {
	key := make([]byte, 16) // AES-128; all-zero is fine for the test
	master, err := newGossipNode(gossipConfig{
		NodeID:    "master",
		Role:      roleMaster,
		APIAddr:   "master:13420",
		BindAddr:  "127.0.0.1:0",
		SecretKey: key,
	})
	require.NoError(t, err)
	defer master.shutdown()

	slave, err := newGossipNode(gossipConfig{
		NodeID:    "slave-1",
		Role:      roleSlave,
		APIAddr:   "slave1:13420",
		BindAddr:  "127.0.0.1:0",
		Seeds:     []string{master.ml.LocalNode().Address()},
		SecretKey: key,
	})
	require.NoError(t, err)
	defer slave.shutdown()

	require.Eventually(t, func() bool {
		_, e := slave.leaderAPIAddr()
		return e == nil
	}, 5*time.Second, 50*time.Millisecond)
}
