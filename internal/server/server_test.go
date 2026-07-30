package server

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNew_DefaultsToCoordinator(t *testing.T) {
	srv, err := New(Config{})
	require.NoError(t, err)
	assert.Equal(t, ModeCoordinator, srv.Mode())
	assert.True(t, srv.LeaderConnected()) // coordinator is always "connected"
}

func TestNew_GeneratesNodeIDWhenEmpty(t *testing.T) {
	// An empty cluster.node_id must yield a generated id — a worker with an
	// empty node_id is rejected by the coordinator's register handler with 400.
	s1, err := New(Config{Mode: ModeWorker})
	require.NoError(t, err)
	assert.NotEmpty(t, s1.NodeID(), "empty node_id must be generated")
	assert.Contains(t, s1.NodeID(), string(ModeWorker), "generated id encodes the mode")

	// Distinct nodes get distinct ids (random suffix).
	s2, err := New(Config{Mode: ModeWorker})
	require.NoError(t, err)
	assert.NotEqual(t, s1.NodeID(), s2.NodeID(), "generated ids must be unique")
}

func TestNew_KeepsExplicitNodeID(t *testing.T) {
	srv, err := New(Config{Mode: ModeWorker, NodeID: "my-node"})
	require.NoError(t, err)
	assert.Equal(t, "my-node", srv.NodeID(), "explicit node_id is preserved")
}

func TestNew_ExplicitCoordinator(t *testing.T) {
	srv, err := New(Config{Mode: ModeCoordinator})
	require.NoError(t, err)
	assert.Equal(t, ModeCoordinator, srv.Mode())
}

func TestNew_Worker(t *testing.T) {
	srv, err := New(Config{Mode: ModeWorker, Leader: "coordinator:13420"})
	require.NoError(t, err)
	assert.Equal(t, ModeWorker, srv.Mode())
	assert.False(t, srv.LeaderConnected()) // not until connectLeader runs
}

func TestNew_InvalidMode(t *testing.T) {
	_, err := New(Config{Mode: Mode("bogus")})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid mode")
}

func TestNew_AgentCommandFallback(t *testing.T) {
	srv, err := New(Config{})
	require.NoError(t, err)
	assert.NotEmpty(t, srv.cfg.AgentCommand)
}

func TestStart_DoubleStart(t *testing.T) {
	srv, err := New(Config{})
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	require.NoError(t, srv.Start(ctx))
	err = srv.Start(ctx)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "already running")
}

func TestHeartbeat_BeforeRegister(t *testing.T) {
	// A coordinator may receive a heartbeat before any register — e.g. after a
	// restart while a worker still believes it is connected. This must not
	// panic on the (previously nil) workers map.
	srv, err := New(Config{Mode: ModeCoordinator, NodeID: "coordinator-1"})
	require.NoError(t, err)

	assert.NotPanics(t, func() {
		leaderID, ok := srv.Heartbeat("worker-x", nil, nil)
		assert.True(t, ok)
		assert.Equal(t, "coordinator-1", leaderID)
	})
}

func TestLocalAddr_AdvertiseAddrOrFallback(t *testing.T) {
	srv, err := New(Config{Mode: ModeWorker, Port: 13420, AdvertiseAddr: "worker1:13420"})
	require.NoError(t, err)
	assert.Equal(t, "worker1:13420", srv.localAddr(), "configured advertise addr is used verbatim")

	srv2, err := New(Config{Mode: ModeWorker, Port: 13421})
	require.NoError(t, err)
	assert.Equal(t, ":13421", srv2.localAddr(), "falls back to :<port> when unset")
}

func TestWorkers_EvictedWhenLongStale(t *testing.T) {
	srv, err := New(Config{Mode: ModeCoordinator, NodeID: "coordinator-1"})
	require.NoError(t, err)
	base := time.Unix(1_700_000_000, 0)
	srv.now = func() time.Time { return base }
	srv.RegisterWorker("worker-1", "worker1:13420")
	require.Len(t, srv.Workers(), 1)

	// Past stale but before evict: still present, marked stale (TUI visibility).
	srv.now = func() time.Time { return base.Add(workerStaleAfter + time.Second) }
	sl := srv.Workers()
	require.Len(t, sl, 1)
	assert.True(t, sl[0].Stale)

	// Past the evict threshold: dropped from the registry.
	srv.now = func() time.Time { return base.Add(workerEvictAfter + time.Second) }
	assert.Empty(t, srv.Workers(), "a long-stale worker should be evicted")
}

func TestRemoteAgentNode(t *testing.T) {
	srv, err := New(Config{Mode: ModeCoordinator, NodeID: "coordinator-1"})
	require.NoError(t, err)
	base := time.Unix(1_700_000_000, 0)
	srv.now = func() time.Time { return base }
	srv.RegisterWorker("worker-1", "worker1:13420")
	srv.ReportContexts("worker-1", []ExecutionContext{{AgentID: "a0-1", NodeID: "worker-1"}})

	addr, ok := srv.RemoteAgentNode("a0-1")
	assert.True(t, ok)
	assert.Equal(t, "worker1:13420", addr)

	_, ok = srv.RemoteAgentNode("no-such-agent")
	assert.False(t, ok, "unknown id does not resolve")

	// A stale node is not routable.
	srv.now = func() time.Time { return base.Add(workerStaleAfter + time.Second) }
	_, ok = srv.RemoteAgentNode("a0-1")
	assert.False(t, ok, "stale node should not be routable")
}

func TestRemoteAgentNode_Ambiguous(t *testing.T) {
	srv, err := New(Config{Mode: ModeCoordinator, NodeID: "coordinator-1"})
	require.NoError(t, err)
	base := time.Unix(1_700_000_000, 0)
	srv.now = func() time.Time { return base }
	srv.RegisterWorker("worker-1", "worker1:13420")
	srv.RegisterWorker("worker-2", "worker2:13420")
	srv.ReportContexts("worker-1", []ExecutionContext{{AgentID: "dup", NodeID: "worker-1"}})
	srv.ReportContexts("worker-2", []ExecutionContext{{AgentID: "dup", NodeID: "worker-2"}})

	_, ok := srv.RemoteAgentNode("dup")
	assert.False(t, ok, "an id reported by two nodes must not route")
}

func TestRemoteAgentNode_WorkerModeReturnsFalse(t *testing.T) {
	srv, err := New(Config{Mode: ModeWorker, Leader: "coordinator:13420"})
	require.NoError(t, err)
	_, ok := srv.RemoteAgentNode("anything")
	assert.False(t, ok, "a worker holds no remote registry")
}

func TestWorkers_MarkedStale(t *testing.T) {
	srv, err := New(Config{Mode: ModeCoordinator, NodeID: "coordinator-1"})
	require.NoError(t, err)

	base := time.Unix(1_700_000_000, 0)
	srv.now = func() time.Time { return base }
	srv.RegisterWorker("worker-1", "worker1:13420")

	// Fresh registration is not stale.
	workers := srv.Workers()
	require.Len(t, workers, 1)
	assert.False(t, workers[0].Stale)

	// Advance the clock past the staleness window without a heartbeat.
	srv.now = func() time.Time { return base.Add(workerStaleAfter + time.Second) }
	workers = srv.Workers()
	require.Len(t, workers, 1)
	assert.True(t, workers[0].Stale)

	// A heartbeat refreshes last-seen and clears staleness.
	srv.Heartbeat("worker-1", []string{"greeter"}, nil)
	workers = srv.Workers()
	require.Len(t, workers, 1)
	assert.False(t, workers[0].Stale)
	assert.Equal(t, []string{"greeter"}, workers[0].Agents)
}

func TestStart_WorkerWithoutLeader(t *testing.T) {
	srv, err := New(Config{Mode: ModeWorker})
	require.NoError(t, err)
	assert.NotPanics(t, func() {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		_ = srv.Start(ctx)
	})
}
