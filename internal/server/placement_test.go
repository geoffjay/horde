package server

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveSpawnTarget_LocalRequests(t *testing.T) {
	srv, err := New(Config{Mode: ModeMaster, NodeID: "master-1"})
	require.NoError(t, err)

	for _, requested := range []string{"", nodeLocal, "master-1"} {
		addr, local, rErr := srv.ResolveSpawnTarget(requested)
		require.NoError(t, rErr)
		assert.True(t, local, "%q should resolve to the local node", requested)
		assert.Empty(t, addr)
	}
}

func TestResolveSpawnTarget_ExplicitSlave(t *testing.T) {
	srv, err := New(Config{Mode: ModeMaster, NodeID: "master-1"})
	require.NoError(t, err)
	base := time.Unix(1_700_000_000, 0)
	srv.now = func() time.Time { return base }
	srv.RegisterSlave("slave-1", "slave1:13420")

	addr, local, rErr := srv.ResolveSpawnTarget("slave-1")
	require.NoError(t, rErr)
	assert.False(t, local)
	assert.Equal(t, "slave1:13420", addr)

	// Unknown node id.
	_, _, rErr = srv.ResolveSpawnTarget("no-such-node")
	assert.ErrorIs(t, rErr, ErrNodeNotFound)

	// Stale node is not a valid target.
	srv.now = func() time.Time { return base.Add(slaveStaleAfter + time.Second) }
	_, _, rErr = srv.ResolveSpawnTarget("slave-1")
	assert.ErrorIs(t, rErr, ErrNodeNotFound, "a stale slave is not a placement target")
}

func TestResolveSpawnTarget_AutoPicksLeastLoaded(t *testing.T) {
	srv, err := New(Config{Mode: ModeMaster, NodeID: "master-1"})
	require.NoError(t, err)
	base := time.Unix(1_700_000_000, 0)
	srv.now = func() time.Time { return base }

	// No slaves: auto falls back to local.
	addr, local, rErr := srv.ResolveSpawnTarget(nodeAuto)
	require.NoError(t, rErr)
	assert.True(t, local, "auto with no slaves spawns locally")
	assert.Empty(t, addr)

	// Local node is more loaded than an idle slave: auto picks the slave.
	srv.procs["a-local-1"] = &agentProc{id: "a-local-1"}
	srv.procs["a-local-2"] = &agentProc{id: "a-local-2"}
	srv.RegisterSlave("slave-1", "slave1:13420")
	srv.Heartbeat("slave-1", nil, nil) // zero agents reported

	addr, local, rErr = srv.ResolveSpawnTarget(nodeAuto)
	require.NoError(t, rErr)
	assert.False(t, local, "an idle slave beats a loaded local node")
	assert.Equal(t, "slave1:13420", addr)

	// A slave busier than local: auto prefers local (tie/less → local).
	srv.Heartbeat("slave-1", []string{"a", "b", "c"}, nil)
	_, local, rErr = srv.ResolveSpawnTarget(nodeAuto)
	require.NoError(t, rErr)
	assert.True(t, local, "local wins when it is no more loaded than every slave")
}

func TestResolveSpawnTarget_AutoSkipsStaleSlave(t *testing.T) {
	srv, err := New(Config{Mode: ModeMaster, NodeID: "master-1"})
	require.NoError(t, err)
	base := time.Unix(1_700_000_000, 0)
	srv.now = func() time.Time { return base }
	srv.procs["a-local-1"] = &agentProc{id: "a-local-1"}
	srv.RegisterSlave("slave-1", "slave1:13420")

	// Slave goes stale (but not yet evicted): auto must not target it.
	srv.now = func() time.Time { return base.Add(slaveStaleAfter + time.Second) }
	_, local, rErr := srv.ResolveSpawnTarget(nodeAuto)
	require.NoError(t, rErr)
	assert.True(t, local, "a stale slave is not an auto-placement candidate")
}

func TestResolveSpawnTarget_RemotePlacementIsMasterOnly(t *testing.T) {
	srv, err := New(Config{Mode: ModeSlave, NodeID: "slave-1", Leader: "master:13420"})
	require.NoError(t, err)

	// Local requests still work on a slave.
	_, local, rErr := srv.ResolveSpawnTarget("")
	require.NoError(t, rErr)
	assert.True(t, local)

	// A slave cannot place agents on other nodes.
	_, _, rErr = srv.ResolveSpawnTarget(nodeAuto)
	assert.ErrorIs(t, rErr, ErrPlacementMasterOnly)
	_, _, rErr = srv.ResolveSpawnTarget("some-other-node")
	assert.ErrorIs(t, rErr, ErrPlacementMasterOnly)
}
