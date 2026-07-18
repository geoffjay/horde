//go:build integration

package server_test

import (
	"context"
	"net"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/geoffjay/horde/internal/api"
	"github.com/geoffjay/horde/internal/client"
	"github.com/geoffjay/horde/internal/server"
)

// This file exhaustively exercises the Phase 4 distributed features against
// real in-process nodes (real HTTP + memberlist + subprocess agents on
// loopback): cross-node invoke, placement, slave→master invoke forwarding,
// cluster auth, event fan-out, and gossip discovery/encryption. It is the
// repeatable counterpart to the ad-hoc scripts used during development.

// freePort returns a likely-free localhost TCP port (closed immediately). Used
// to pre-allocate gossip bind/seed ports before a node starts.
func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := ln.Addr().(*net.TCPAddr).Port
	require.NoError(t, ln.Close())
	return port
}

// startNode starts an in-process node serving the API on a real loopback
// listener. cfg.Port and (unless set) cfg.AdvertiseAddr are filled from the
// listener so peers can route to it. It returns the node, its API address, and
// a client; cleanup stops agents and the server at test end.
func startNode(t *testing.T, cfg server.Config) (*server.Server, string, *client.Client) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	cfg.Port = ln.Addr().(*net.TCPAddr).Port
	if cfg.AdvertiseAddr == "" {
		cfg.AdvertiseAddr = addr
	}

	srv, err := server.New(cfg)
	require.NoError(t, err)

	httpSrv := &http.Server{Handler: api.Router(srv)} //nolint:gosec // test server, no timeouts needed
	go func() { _ = httpSrv.Serve(ln) }()

	ctx, cancel := context.WithCancel(context.Background())
	require.NoError(t, srv.Start(ctx))
	t.Cleanup(func() {
		for _, a := range srv.Agents() {
			_ = srv.StopAgent(a.ID)
		}
		cancel()
		_ = httpSrv.Close()
	})

	c := client.New(addr)
	require.Eventually(t, func() bool { return c.Health(context.Background()) == nil },
		5*time.Second, 20*time.Millisecond, "node did not become healthy")
	return srv, addr, c
}

// requireSlaveRegistered blocks until the master's cluster view lists nodeID as
// a non-stale slave.
func requireSlaveRegistered(t *testing.T, mc *client.Client, nodeID string) {
	t.Helper()
	require.Eventually(t, func() bool {
		v, err := mc.ListNodes(context.Background())
		if err != nil {
			return false
		}
		for _, n := range v.Nodes {
			if n.NodeID == nodeID && !n.Stale {
				return true
			}
		}
		return false
	}, 15*time.Second, 250*time.Millisecond, "slave %q did not register with the master", nodeID)
}

// assertInvokeReply invokes the agent through c and asserts the greeter reply
// streams back.
func assertInvokeReply(t *testing.T, c *client.Client, agentID, message string) {
	t.Helper()
	ch, err := c.Invoke(context.Background(), agentID, client.InvokeRequest{Message: message})
	require.NoError(t, err)
	var got string
	for ev := range ch {
		got += string(ev.Data)
	}
	assert.Contains(t, got, "Hello from horde", "expected the greeter reply in the invoke stream")
}

func masterCfg(bin string) server.Config {
	return server.Config{Mode: server.ModeMaster, NodeID: "master", AgentCommand: bin, SpawnDefaultAgent: false}
}

func slaveCfg(bin, leader string) server.Config {
	return server.Config{Mode: server.ModeSlave, NodeID: "slave-1", Leader: leader, AgentCommand: bin, SpawnDefaultAgent: false}
}

// TestCluster_CrossNodeInvoke: an agent spawned on the slave is invokable
// through the master (slice 1).
func TestCluster_CrossNodeInvoke(t *testing.T) {
	bin := findHordeBinary(t)
	_, masterAddr, mc := startNode(t, masterCfg(bin))
	_, _, sc := startNode(t, slaveCfg(bin, masterAddr))

	a, err := sc.SpawnAgent(context.Background(), "greeter", "")
	require.NoError(t, err)

	// The master aggregates the slave's agent via heartbeat digests.
	require.Eventually(t, func() bool {
		ctxs, _ := mc.ListRemoteAgentContexts(context.Background(), "")
		for i := range ctxs {
			if ctxs[i].AgentID == a.ID {
				return true
			}
		}
		return false
	}, 15*time.Second, 250*time.Millisecond, "master did not aggregate the slave agent")

	assertInvokeReply(t, mc, a.ID, "cross node")
}

// TestCluster_Placement: the master places a new agent on a chosen slave
// (slice 2).
func TestCluster_Placement(t *testing.T) {
	bin := findHordeBinary(t)
	_, masterAddr, mc := startNode(t, masterCfg(bin))
	_, _, sc := startNode(t, slaveCfg(bin, masterAddr))
	requireSlaveRegistered(t, mc, "slave-1")

	a, err := mc.SpawnAgent(context.Background(), "greeter", "slave-1")
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		agents, _ := sc.ListAgents(context.Background())
		for _, ag := range agents {
			if ag.ID == a.ID {
				return true
			}
		}
		return false
	}, 5*time.Second, 100*time.Millisecond, "the placed agent should run on the slave")
}

// TestCluster_SlaveForwardsInvokeToMaster: any node is a valid invoke entry
// point — the slave forwards an invoke for an agent it does not host to the
// master, which serves it.
func TestCluster_SlaveForwardsInvokeToMaster(t *testing.T) {
	bin := findHordeBinary(t)
	_, masterAddr, mc := startNode(t, masterCfg(bin))
	_, _, sc := startNode(t, slaveCfg(bin, masterAddr))

	a, err := mc.SpawnAgent(context.Background(), "greeter", "")
	require.NoError(t, err)

	// Invoke through the SLAVE; it forwards to the master.
	assertInvokeReply(t, sc, a.ID, "via slave")
}

// TestCluster_AuthToken: a matching cluster token registers; a wrong token is
// rejected and the slave never appears in the cluster view.
func TestCluster_AuthToken(t *testing.T) {
	const token = "s3cret-cluster-token"
	master := masterCfg("")
	master.AuthToken = token
	_, masterAddr, mc := startNode(t, master)

	good := slaveCfg("", masterAddr)
	good.NodeID = "slave-ok"
	good.AuthToken = token
	startNode(t, good)
	requireSlaveRegistered(t, mc, "slave-ok")

	bad := slaveCfg("", masterAddr)
	bad.NodeID = "slave-bad"
	bad.AuthToken = "wrong-token"
	startNode(t, bad)

	// Give the bad slave time to attempt (and fail) registration, then confirm
	// it is absent.
	time.Sleep(2 * time.Second)
	v, err := mc.ListNodes(context.Background())
	require.NoError(t, err)
	for _, n := range v.Nodes {
		assert.NotEqual(t, "slave-bad", n.NodeID, "a slave with a wrong token must not register")
	}
}

// TestCluster_EventFanOut: a spawn on the slave surfaces on the master's
// cluster-wide event stream with the slave as origin (slice 4).
func TestCluster_EventFanOut(t *testing.T) {
	bin := findHordeBinary(t)
	_, masterAddr, mc := startNode(t, masterCfg(bin))
	_, _, sc := startNode(t, slaveCfg(bin, masterAddr))
	requireSlaveRegistered(t, mc, "slave-1")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events, err := mc.StreamEvents(ctx)
	require.NoError(t, err)

	_, err = sc.SpawnAgent(context.Background(), "greeter", "")
	require.NoError(t, err)

	deadline := time.After(10 * time.Second)
	for {
		select {
		case ev, ok := <-events:
			if !ok {
				t.Fatal("event stream closed before the slave's spawn event arrived")
			}
			if ev.Type == client.EventAgentSpawned && ev.Node == "slave-1" {
				return // success
			}
		case <-deadline:
			t.Fatal("master did not receive the slave's agent.spawned event")
		}
	}
}

// TestCluster_GossipDiscovery: a slave finds the master via gossip (no static
// leader) and registers (slice 5).
func TestCluster_GossipDiscovery(t *testing.T) {
	seed := "127.0.0.1:" + strconv.Itoa(freePort(t))
	master := server.Config{
		Mode: server.ModeMaster, NodeID: "master", SpawnDefaultAgent: false,
		DiscoveryMechanism: "gossip", GossipBindAddr: seed, GossipAdvertiseAddr: seed,
	}
	_, _, mc := startNode(t, master)

	sgossip := "127.0.0.1:" + strconv.Itoa(freePort(t))
	slave := server.Config{
		Mode: server.ModeSlave, NodeID: "slave-1", SpawnDefaultAgent: false,
		DiscoveryMechanism: "gossip", GossipBindAddr: sgossip, GossipAdvertiseAddr: sgossip,
		GossipSeeds: []string{seed},
	}
	startNode(t, slave)

	requireSlaveRegistered(t, mc, "slave-1")
}

// TestCluster_GossipEncryption: gossip with a shared encryption key still
// converges and the slave registers.
func TestCluster_GossipEncryption(t *testing.T) {
	key := make([]byte, 32) // AES-256; all-zero is fine for the test
	seed := "127.0.0.1:" + strconv.Itoa(freePort(t))
	master := server.Config{
		Mode: server.ModeMaster, NodeID: "master", SpawnDefaultAgent: false,
		DiscoveryMechanism: "gossip", GossipBindAddr: seed, GossipAdvertiseAddr: seed,
		GossipEncryptionKey: key,
	}
	_, _, mc := startNode(t, master)

	sgossip := "127.0.0.1:" + strconv.Itoa(freePort(t))
	slave := server.Config{
		Mode: server.ModeSlave, NodeID: "slave-1", SpawnDefaultAgent: false,
		DiscoveryMechanism: "gossip", GossipBindAddr: sgossip, GossipAdvertiseAddr: sgossip,
		GossipSeeds: []string{seed}, GossipEncryptionKey: key,
	}
	startNode(t, slave)

	requireSlaveRegistered(t, mc, "slave-1")
}
