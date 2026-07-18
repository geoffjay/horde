package server

import (
	"net"
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

func TestSplitHostPortDefault(t *testing.T) {
	host, port, err := splitHostPortDefault("0.0.0.0:7946", defaultGossipPort)
	require.NoError(t, err)
	assert.Equal(t, "0.0.0.0", host)
	assert.Equal(t, 7946, port)

	// A bare host defaults the port.
	host, port, err = splitHostPortDefault("master", defaultGossipPort)
	require.NoError(t, err)
	assert.Equal(t, "master", host)
	assert.Equal(t, defaultGossipPort, port)

	// A non-numeric port is an error.
	_, _, err = splitHostPortDefault("host:abc", defaultGossipPort)
	assert.Error(t, err)
}

func TestResolveHostIP(t *testing.T) {
	// An IP passes through unchanged.
	ip, err := resolveHostIP("127.0.0.1")
	require.NoError(t, err)
	assert.Equal(t, "127.0.0.1", ip)

	// A hostname resolves to an address (localhost always resolves).
	ip, err = resolveHostIP("localhost")
	require.NoError(t, err)
	assert.NotEmpty(t, ip)
	assert.NotNil(t, net.ParseIP(ip), "resolved value is an IP")
}
