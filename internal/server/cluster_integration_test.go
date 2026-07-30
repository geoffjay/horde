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
// loopback): cross-node invoke, placement, worker→coordinator invoke forwarding,
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

// requireWorkerRegistered blocks until the coordinator's cluster view lists nodeID as
// a non-stale worker.
func requireWorkerRegistered(t *testing.T, mc *client.Client, nodeID string) {
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
	}, 15*time.Second, 250*time.Millisecond, "worker %q did not register with the coordinator", nodeID)
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

func coordinatorCfg(bin string) server.Config {
	return server.Config{Mode: server.ModeCoordinator, NodeID: "coordinator", AgentCommand: bin, SpawnDefaultAgent: false}
}

func workerCfg(bin, leader string) server.Config {
	return server.Config{Mode: server.ModeWorker, NodeID: "worker-1", Leader: leader, AgentCommand: bin, SpawnDefaultAgent: false}
}

// TestCluster_CrossNodeInvoke: an agent spawned on the worker is invokable
// through the coordinator (slice 1).
func TestCluster_CrossNodeInvoke(t *testing.T) {
	bin := findHordeBinary(t)
	_, coordinatorAddr, mc := startNode(t, coordinatorCfg(bin))
	_, _, sc := startNode(t, workerCfg(bin, coordinatorAddr))

	a, err := sc.SpawnAgent(context.Background(), "greeter", "")
	require.NoError(t, err)

	// The coordinator aggregates the worker's agent via heartbeat digests.
	require.Eventually(t, func() bool {
		ctxs, _ := mc.ListRemoteAgentContexts(context.Background(), "")
		for i := range ctxs {
			if ctxs[i].AgentID == a.ID {
				return true
			}
		}
		return false
	}, 15*time.Second, 250*time.Millisecond, "coordinator did not aggregate the worker agent")

	assertInvokeReply(t, mc, a.ID, "cross node")
}

// TestCluster_Placement: the coordinator places a new agent on a chosen worker
// (slice 2).
func TestCluster_Placement(t *testing.T) {
	bin := findHordeBinary(t)
	_, coordinatorAddr, mc := startNode(t, coordinatorCfg(bin))
	_, _, sc := startNode(t, workerCfg(bin, coordinatorAddr))
	requireWorkerRegistered(t, mc, "worker-1")

	a, err := mc.SpawnAgent(context.Background(), "greeter", "worker-1")
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		agents, _ := sc.ListAgents(context.Background())
		for _, ag := range agents {
			if ag.ID == a.ID {
				return true
			}
		}
		return false
	}, 5*time.Second, 100*time.Millisecond, "the placed agent should run on the worker")
}

// TestCluster_WorkerForwardsInvokeToCoordinator: any node is a valid invoke entry
// point — the worker forwards an invoke for an agent it does not host to the
// coordinator, which serves it.
func TestCluster_WorkerForwardsInvokeToCoordinator(t *testing.T) {
	bin := findHordeBinary(t)
	_, coordinatorAddr, mc := startNode(t, coordinatorCfg(bin))
	_, _, sc := startNode(t, workerCfg(bin, coordinatorAddr))

	a, err := mc.SpawnAgent(context.Background(), "greeter", "")
	require.NoError(t, err)

	// Invoke through the WORKER; it forwards to the coordinator.
	assertInvokeReply(t, sc, a.ID, "via worker")
}

// TestCluster_AuthToken: a matching cluster token registers; a wrong token is
// rejected and the worker never appears in the cluster view.
func TestCluster_AuthToken(t *testing.T) {
	const token = "s3cret-cluster-token"
	coordinator := coordinatorCfg("")
	coordinator.AuthToken = token
	_, coordinatorAddr, mc := startNode(t, coordinator)

	good := workerCfg("", coordinatorAddr)
	good.NodeID = "worker-ok"
	good.AuthToken = token
	startNode(t, good)
	requireWorkerRegistered(t, mc, "worker-ok")

	bad := workerCfg("", coordinatorAddr)
	bad.NodeID = "worker-bad"
	bad.AuthToken = "wrong-token"
	startNode(t, bad)

	// Give the bad worker time to attempt (and fail) registration, then confirm
	// it is absent.
	time.Sleep(2 * time.Second)
	v, err := mc.ListNodes(context.Background())
	require.NoError(t, err)
	for _, n := range v.Nodes {
		assert.NotEqual(t, "worker-bad", n.NodeID, "a worker with a wrong token must not register")
	}
}

// TestCluster_EventFanOut: a spawn on the worker surfaces on the coordinator's
// cluster-wide event stream with the worker as origin (slice 4).
func TestCluster_EventFanOut(t *testing.T) {
	bin := findHordeBinary(t)
	_, coordinatorAddr, mc := startNode(t, coordinatorCfg(bin))
	_, _, sc := startNode(t, workerCfg(bin, coordinatorAddr))
	requireWorkerRegistered(t, mc, "worker-1")

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
				t.Fatal("event stream closed before the worker's spawn event arrived")
			}
			if ev.Type == client.EventAgentSpawned && ev.Node == "worker-1" {
				return // success
			}
		case <-deadline:
			t.Fatal("coordinator did not receive the worker's agent.spawned event")
		}
	}
}

// TestCluster_GossipDiscovery: a worker finds the coordinator via gossip (no static
// leader) and registers (slice 5).
func TestCluster_GossipDiscovery(t *testing.T) {
	seed := "127.0.0.1:" + strconv.Itoa(freePort(t))
	coordinator := server.Config{
		Mode: server.ModeCoordinator, NodeID: "coordinator", SpawnDefaultAgent: false,
		DiscoveryMechanism: "gossip", GossipBindAddr: seed, GossipAdvertiseAddr: seed,
	}
	_, _, mc := startNode(t, coordinator)

	sgossip := "127.0.0.1:" + strconv.Itoa(freePort(t))
	worker := server.Config{
		Mode: server.ModeWorker, NodeID: "worker-1", SpawnDefaultAgent: false,
		DiscoveryMechanism: "gossip", GossipBindAddr: sgossip, GossipAdvertiseAddr: sgossip,
		GossipSeeds: []string{seed},
	}
	startNode(t, worker)

	requireWorkerRegistered(t, mc, "worker-1")
}

// TestCluster_GossipEncryption: gossip with a shared encryption key still
// converges and the worker registers.
func TestCluster_GossipEncryption(t *testing.T) {
	key := make([]byte, 32) // AES-256; all-zero is fine for the test
	seed := "127.0.0.1:" + strconv.Itoa(freePort(t))
	coordinator := server.Config{
		Mode: server.ModeCoordinator, NodeID: "coordinator", SpawnDefaultAgent: false,
		DiscoveryMechanism: "gossip", GossipBindAddr: seed, GossipAdvertiseAddr: seed,
		GossipEncryptionKey: key,
	}
	_, _, mc := startNode(t, coordinator)

	sgossip := "127.0.0.1:" + strconv.Itoa(freePort(t))
	worker := server.Config{
		Mode: server.ModeWorker, NodeID: "worker-1", SpawnDefaultAgent: false,
		DiscoveryMechanism: "gossip", GossipBindAddr: sgossip, GossipAdvertiseAddr: sgossip,
		GossipSeeds: []string{seed}, GossipEncryptionKey: key,
	}
	startNode(t, worker)

	requireWorkerRegistered(t, mc, "worker-1")
}
