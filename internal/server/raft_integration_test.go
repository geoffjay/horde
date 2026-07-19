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

// This file exercises raft leader failover (Phase 5) against real in-process
// nodes: a 3-node raft cluster over gossip elects one leader, followers register
// with it, and killing the leader triggers a re-election that survivors follow.

// raftNodeHandle is a running failover node the test can stop independently to
// simulate a leader crash.
type raftNodeHandle struct {
	srv     *server.Server
	addr    string // gossip address (a seed for followers)
	apiAddr string // HTTP API address
	client  *client.Client
	stop    func()
}

// startRaftNode starts an in-process failover node (raft + gossip) on loopback,
// with its own raft data dir and pre-allocated gossip/raft ports. seeds is the
// gossip seed list (empty for the bootstrap master). It returns a handle whose
// stop() crashes just this node (cancels its context and closes its listener).
func startRaftNode(t *testing.T, nodeID string, master bool, seeds []string) raftNodeHandle {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	apiAddr := ln.Addr().String()

	gossipAddr := "127.0.0.1:" + strconv.Itoa(freePort(t))
	raftAddr := "127.0.0.1:" + strconv.Itoa(freePort(t))

	mode := server.ModeSlave
	if master {
		mode = server.ModeMaster
	}
	cfg := server.Config{
		Mode:                mode,
		NodeID:              nodeID,
		AgentCommand:        findHordeBinary(t),
		SpawnDefaultAgent:   false,
		Port:                ln.Addr().(*net.TCPAddr).Port,
		AdvertiseAddr:       apiAddr,
		DiscoveryMechanism:  "gossip",
		GossipBindAddr:      gossipAddr,
		GossipAdvertiseAddr: gossipAddr,
		GossipSeeds:         seeds,
		Failover:            server.FailoverRaft,
		RaftBindAddr:        raftAddr,
		RaftAdvertiseAddr:   raftAddr,
		RaftDir:             t.TempDir(),
	}

	srv, err := server.New(cfg)
	require.NoError(t, err)

	httpSrv := &http.Server{Handler: api.Router(srv)} //nolint:gosec // test server
	go func() { _ = httpSrv.Serve(ln) }()

	ctx, cancel := context.WithCancel(context.Background())
	require.NoError(t, srv.Start(ctx))

	var stopped bool
	stop := func() {
		if stopped {
			return
		}
		stopped = true
		// Stop the node's agent subprocesses so a crashed node leaves no leaked
		// children holding the test's I/O pipe (a real crash would orphan them;
		// the test cleans them up explicitly).
		for _, a := range srv.Agents() {
			_ = srv.StopAgent(a.ID)
		}
		cancel()
		_ = httpSrv.Close()
	}
	t.Cleanup(stop)

	c := client.New(apiAddr)
	// Report the gossip addr so the caller can seed followers off the master.
	return raftNodeHandle{srv: srv, addr: gossipAddr, apiAddr: apiAddr, client: c, stop: stop}
}

// awaitLeader blocks until exactly one of the given live nodes reports raft
// leadership, and returns it. Fails the test on timeout.
func awaitLeader(t *testing.T, nodes ...*server.Server) *server.Server {
	t.Helper()
	var leader *server.Server
	require.Eventually(t, func() bool {
		leader = nil
		count := 0
		for _, n := range nodes {
			if n.IsLeader() {
				leader = n
				count++
			}
		}
		return count == 1
	}, 30*time.Second, 200*time.Millisecond, "expected exactly one raft leader")
	return leader
}

// TestRaftFailover_ElectsSingleLeader: a 3-node raft cluster over gossip elects
// exactly one leader, and the two followers register with it.
func TestRaftFailover_ElectsSingleLeader(t *testing.T) {
	m := startRaftNode(t, "node-a", true, nil)
	f1 := startRaftNode(t, "node-b", false, []string{m.addr})
	f2 := startRaftNode(t, "node-c", false, []string{m.addr})

	leader := awaitLeader(t, m.srv, f1.srv, f2.srv)
	assert.Equal(t, "node-a", leader.NodeID(), "the bootstrap node should lead initially")

	// Both followers register with the leader (its cluster view lists 2 nodes).
	require.Eventually(t, func() bool {
		v, err := m.client.ListNodes(context.Background())
		if err != nil {
			return false
		}
		fresh := 0
		for _, n := range v.Nodes {
			if !n.Stale {
				fresh++
			}
		}
		return fresh == 2
	}, 30*time.Second, 250*time.Millisecond, "both followers should register with the leader")
}

// TestRaftFailover_ReElectsOnLeaderCrash: killing the leader triggers a
// re-election among the survivors, and a survivor follows the new leader.
func TestRaftFailover_ReElectsOnLeaderCrash(t *testing.T) {
	m := startRaftNode(t, "node-a", true, nil)
	f1 := startRaftNode(t, "node-b", false, []string{m.addr})
	f2 := startRaftNode(t, "node-c", false, []string{m.addr})

	leader := awaitLeader(t, m.srv, f1.srv, f2.srv)
	require.Equal(t, "node-a", leader.NodeID())

	// Wait for both followers to be voters so the surviving two form a quorum.
	require.Eventually(t, func() bool {
		v, err := m.client.ListNodes(context.Background())
		if err != nil {
			return false
		}
		fresh := 0
		for _, n := range v.Nodes {
			if !n.Stale {
				fresh++
			}
		}
		return fresh == 2
	}, 30*time.Second, 250*time.Millisecond, "both followers registered before crash")

	// Crash the leader (node-a).
	m.stop()

	// The two survivors re-elect a leader among themselves.
	newLeader := awaitLeader(t, f1.srv, f2.srv)
	assert.NotEqual(t, "node-a", newLeader.NodeID(), "a survivor should become the new leader")

	// The other survivor follows the new leader: its cluster view lists the peer.
	var follower raftNodeHandle
	if newLeader.NodeID() == "node-b" {
		follower = f1
	} else {
		follower = f2
	}
	require.Eventually(t, func() bool {
		v, err := follower.client.ListNodes(context.Background())
		if err != nil {
			return false
		}
		for _, n := range v.Nodes {
			if !n.Stale {
				return true
			}
		}
		return false
	}, 30*time.Second, 250*time.Millisecond, "the surviving follower should register with the new leader")
}

// TestRaftFailover_ProjectStateSurvivesFailover: a project created (and paused)
// on the leader is replicated through the raft log, so after the leader crashes
// the new leader serves the same project with the same state (slice 2).
func TestRaftFailover_ProjectStateSurvivesFailover(t *testing.T) {
	m := startRaftNode(t, "node-a", true, nil)
	f1 := startRaftNode(t, "node-b", false, []string{m.addr})
	f2 := startRaftNode(t, "node-c", false, []string{m.addr})

	leader := awaitLeader(t, m.srv, f1.srv, f2.srv)
	require.Equal(t, "node-a", leader.NodeID())

	// Create a project on the leader (spawns the greeter locally) and pause it.
	proj, err := m.client.CreateProject(context.Background(), client.CreateProjectRequest{
		Name: "failover-proj", AgentNames: []string{"greeter"},
	})
	require.NoError(t, err)
	require.NotEmpty(t, proj.ID)

	paused, err := m.client.PauseProject(context.Background(), proj.ID)
	require.NoError(t, err)
	require.Equal(t, "paused", paused.State)

	// Ensure both followers are voters so the survivors keep quorum after the crash.
	require.Eventually(t, func() bool {
		v, err := m.client.ListNodes(context.Background())
		if err != nil {
			return false
		}
		fresh := 0
		for _, n := range v.Nodes {
			if !n.Stale {
				fresh++
			}
		}
		return fresh == 2
	}, 30*time.Second, 250*time.Millisecond, "both followers registered before crash")

	// Crash the leader.
	m.stop()
	newLeader := awaitLeader(t, f1.srv, f2.srv)
	require.NotEqual(t, "node-a", newLeader.NodeID())

	// Query the new leader directly (its applied FSM state), avoiding any
	// forwarding: the project survived with its paused state.
	require.Eventually(t, func() bool {
		p, gerr := newLeader.GetProject(proj.ID)
		return gerr == nil && p != nil && p.State == server.ProjectPaused && p.Name == "failover-proj"
	}, 20*time.Second, 250*time.Millisecond, "the project should survive failover with its state")
}

// TestRaftFailover_ClientFollowsLeader: a client seeded with all member
// addresses keeps working across a leader crash — it rotates past the dead node
// to a survivor, which serves (or forwards to the new leader) the request
// (slice 4).
func TestRaftFailover_ClientFollowsLeader(t *testing.T) {
	m := startRaftNode(t, "node-a", true, nil)
	f1 := startRaftNode(t, "node-b", false, []string{m.addr})
	f2 := startRaftNode(t, "node-c", false, []string{m.addr})

	leader := awaitLeader(t, m.srv, f1.srv, f2.srv)
	require.Equal(t, "node-a", leader.NodeID())
	require.Eventually(t, func() bool {
		v, err := m.client.ListNodes(context.Background())
		if err != nil {
			return false
		}
		fresh := 0
		for _, n := range v.Nodes {
			if !n.Stale {
				fresh++
			}
		}
		return fresh == 2
	}, 30*time.Second, 250*time.Millisecond, "both followers registered before crash")

	// A client seeded with every member address.
	cluster := client.NewCluster([]string{m.apiAddr, f1.apiAddr, f2.apiAddr})
	proj, err := cluster.CreateProject(context.Background(), client.CreateProjectRequest{
		Name: "haproj", AgentNames: []string{"greeter"},
	})
	require.NoError(t, err)

	// Crash the leader (node-a). The client should rotate past it to a survivor,
	// which forwards to (or is) the new leader and returns the project.
	m.stop()
	awaitLeader(t, f1.srv, f2.srv)

	require.Eventually(t, func() bool {
		got, gerr := cluster.GetProject(context.Background(), proj.ID)
		return gerr == nil && got.Name == "haproj"
	}, 30*time.Second, 500*time.Millisecond, "the client should follow leadership to a survivor")
}
