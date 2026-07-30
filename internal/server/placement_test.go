package server

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveSpawnTarget_LocalRequests(t *testing.T) {
	srv, err := New(Config{Mode: ModeCoordinator, NodeID: "coordinator-1"})
	require.NoError(t, err)

	for _, requested := range []string{"", nodeLocal, "coordinator-1"} {
		addr, local, rErr := srv.ResolveSpawnTarget(requested)
		require.NoError(t, rErr)
		assert.True(t, local, "%q should resolve to the local node", requested)
		assert.Empty(t, addr)
	}
}

func TestResolveSpawnTarget_ExplicitWorker(t *testing.T) {
	srv, err := New(Config{Mode: ModeCoordinator, NodeID: "coordinator-1"})
	require.NoError(t, err)
	base := time.Unix(1_700_000_000, 0)
	srv.now = func() time.Time { return base }
	srv.RegisterWorker("worker-1", "worker1:13420")

	addr, local, rErr := srv.ResolveSpawnTarget("worker-1")
	require.NoError(t, rErr)
	assert.False(t, local)
	assert.Equal(t, "worker1:13420", addr)

	// Unknown node id.
	_, _, rErr = srv.ResolveSpawnTarget("no-such-node")
	assert.ErrorIs(t, rErr, ErrNodeNotFound)

	// Stale node is not a valid target.
	srv.now = func() time.Time { return base.Add(workerStaleAfter + time.Second) }
	_, _, rErr = srv.ResolveSpawnTarget("worker-1")
	assert.ErrorIs(t, rErr, ErrNodeNotFound, "a stale worker is not a placement target")
}

func TestResolveSpawnTarget_AutoPicksLeastLoaded(t *testing.T) {
	srv, err := New(Config{Mode: ModeCoordinator, NodeID: "coordinator-1"})
	require.NoError(t, err)
	base := time.Unix(1_700_000_000, 0)
	srv.now = func() time.Time { return base }

	// No workers: auto falls back to local.
	addr, local, rErr := srv.ResolveSpawnTarget(nodeAuto)
	require.NoError(t, rErr)
	assert.True(t, local, "auto with no workers spawns locally")
	assert.Empty(t, addr)

	// Local node is more loaded than an idle worker: auto picks the worker.
	srv.procs["a-local-1"] = &agentProc{id: "a-local-1"}
	srv.procs["a-local-2"] = &agentProc{id: "a-local-2"}
	srv.RegisterWorker("worker-1", "worker1:13420")
	srv.Heartbeat("worker-1", nil, nil) // zero agents reported

	addr, local, rErr = srv.ResolveSpawnTarget(nodeAuto)
	require.NoError(t, rErr)
	assert.False(t, local, "an idle worker beats a loaded local node")
	assert.Equal(t, "worker1:13420", addr)

	// A worker busier than local: auto prefers local (tie/less → local).
	srv.Heartbeat("worker-1", []string{"a", "b", "c"}, nil)
	_, local, rErr = srv.ResolveSpawnTarget(nodeAuto)
	require.NoError(t, rErr)
	assert.True(t, local, "local wins when it is no more loaded than every worker")
}

func TestResolveSpawnTarget_AutoSkipsStaleWorker(t *testing.T) {
	srv, err := New(Config{Mode: ModeCoordinator, NodeID: "coordinator-1"})
	require.NoError(t, err)
	base := time.Unix(1_700_000_000, 0)
	srv.now = func() time.Time { return base }
	srv.procs["a-local-1"] = &agentProc{id: "a-local-1"}
	srv.RegisterWorker("worker-1", "worker1:13420")

	// Worker goes stale (but not yet evicted): auto must not target it.
	srv.now = func() time.Time { return base.Add(workerStaleAfter + time.Second) }
	_, local, rErr := srv.ResolveSpawnTarget(nodeAuto)
	require.NoError(t, rErr)
	assert.True(t, local, "a stale worker is not an auto-placement candidate")
}

func TestResolveSpawnTarget_RemotePlacementIsCoordinatorOnly(t *testing.T) {
	srv, err := New(Config{Mode: ModeWorker, NodeID: "worker-1", Leader: "coordinator:13420"})
	require.NoError(t, err)

	// Local requests still work on a worker.
	_, local, rErr := srv.ResolveSpawnTarget("")
	require.NoError(t, rErr)
	assert.True(t, local)

	// A worker cannot place agents on other nodes.
	_, _, rErr = srv.ResolveSpawnTarget(nodeAuto)
	assert.ErrorIs(t, rErr, ErrPlacementCoordinatorOnly)
	_, _, rErr = srv.ResolveSpawnTarget("some-other-node")
	assert.ErrorIs(t, rErr, ErrPlacementCoordinatorOnly)
}
