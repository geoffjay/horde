package server

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNew_DefaultsToMaster(t *testing.T) {
	srv, err := New(Config{})
	require.NoError(t, err)
	assert.Equal(t, ModeMaster, srv.Mode())
	assert.True(t, srv.LeaderConnected()) // master is always "connected"
}

func TestNew_ExplicitMaster(t *testing.T) {
	srv, err := New(Config{Mode: ModeMaster})
	require.NoError(t, err)
	assert.Equal(t, ModeMaster, srv.Mode())
}

func TestNew_Slave(t *testing.T) {
	srv, err := New(Config{Mode: ModeSlave, Leader: "master:13420"})
	require.NoError(t, err)
	assert.Equal(t, ModeSlave, srv.Mode())
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
	// A master may receive a heartbeat before any register — e.g. after a
	// restart while a slave still believes it is connected. This must not
	// panic on the (previously nil) slaves map.
	srv, err := New(Config{Mode: ModeMaster, NodeID: "master-1"})
	require.NoError(t, err)

	assert.NotPanics(t, func() {
		leaderID, ok := srv.Heartbeat("slave-x", nil, nil)
		assert.True(t, ok)
		assert.Equal(t, "master-1", leaderID)
	})
}

func TestLocalAddr_AdvertiseAddrOrFallback(t *testing.T) {
	srv, err := New(Config{Mode: ModeSlave, Port: 13420, AdvertiseAddr: "slave1:13420"})
	require.NoError(t, err)
	assert.Equal(t, "slave1:13420", srv.localAddr(), "configured advertise addr is used verbatim")

	srv2, err := New(Config{Mode: ModeSlave, Port: 13421})
	require.NoError(t, err)
	assert.Equal(t, ":13421", srv2.localAddr(), "falls back to :<port> when unset")
}

func TestSlaves_EvictedWhenLongStale(t *testing.T) {
	srv, err := New(Config{Mode: ModeMaster, NodeID: "master-1"})
	require.NoError(t, err)
	base := time.Unix(1_700_000_000, 0)
	srv.now = func() time.Time { return base }
	srv.RegisterSlave("slave-1", "slave1:13420")
	require.Len(t, srv.Slaves(), 1)

	// Past stale but before evict: still present, marked stale (TUI visibility).
	srv.now = func() time.Time { return base.Add(slaveStaleAfter + time.Second) }
	sl := srv.Slaves()
	require.Len(t, sl, 1)
	assert.True(t, sl[0].Stale)

	// Past the evict threshold: dropped from the registry.
	srv.now = func() time.Time { return base.Add(slaveEvictAfter + time.Second) }
	assert.Empty(t, srv.Slaves(), "a long-stale slave should be evicted")
}

func TestRemoteAgentNode(t *testing.T) {
	srv, err := New(Config{Mode: ModeMaster, NodeID: "master-1"})
	require.NoError(t, err)
	base := time.Unix(1_700_000_000, 0)
	srv.now = func() time.Time { return base }
	srv.RegisterSlave("slave-1", "slave1:13420")
	srv.ReportContexts("slave-1", []ExecutionContext{{AgentID: "a0-1", NodeID: "slave-1"}})

	addr, ok := srv.RemoteAgentNode("a0-1")
	assert.True(t, ok)
	assert.Equal(t, "slave1:13420", addr)

	_, ok = srv.RemoteAgentNode("no-such-agent")
	assert.False(t, ok, "unknown id does not resolve")

	// A stale node is not routable.
	srv.now = func() time.Time { return base.Add(slaveStaleAfter + time.Second) }
	_, ok = srv.RemoteAgentNode("a0-1")
	assert.False(t, ok, "stale node should not be routable")
}

func TestRemoteAgentNode_Ambiguous(t *testing.T) {
	srv, err := New(Config{Mode: ModeMaster, NodeID: "master-1"})
	require.NoError(t, err)
	base := time.Unix(1_700_000_000, 0)
	srv.now = func() time.Time { return base }
	srv.RegisterSlave("slave-1", "slave1:13420")
	srv.RegisterSlave("slave-2", "slave2:13420")
	srv.ReportContexts("slave-1", []ExecutionContext{{AgentID: "dup", NodeID: "slave-1"}})
	srv.ReportContexts("slave-2", []ExecutionContext{{AgentID: "dup", NodeID: "slave-2"}})

	_, ok := srv.RemoteAgentNode("dup")
	assert.False(t, ok, "an id reported by two nodes must not route")
}

func TestRemoteAgentNode_SlaveModeReturnsFalse(t *testing.T) {
	srv, err := New(Config{Mode: ModeSlave, Leader: "master:13420"})
	require.NoError(t, err)
	_, ok := srv.RemoteAgentNode("anything")
	assert.False(t, ok, "a slave holds no remote registry")
}

func TestSlaves_MarkedStale(t *testing.T) {
	srv, err := New(Config{Mode: ModeMaster, NodeID: "master-1"})
	require.NoError(t, err)

	base := time.Unix(1_700_000_000, 0)
	srv.now = func() time.Time { return base }
	srv.RegisterSlave("slave-1", "slave1:13420")

	// Fresh registration is not stale.
	slaves := srv.Slaves()
	require.Len(t, slaves, 1)
	assert.False(t, slaves[0].Stale)

	// Advance the clock past the staleness window without a heartbeat.
	srv.now = func() time.Time { return base.Add(slaveStaleAfter + time.Second) }
	slaves = srv.Slaves()
	require.Len(t, slaves, 1)
	assert.True(t, slaves[0].Stale)

	// A heartbeat refreshes last-seen and clears staleness.
	srv.Heartbeat("slave-1", []string{"greeter"}, nil)
	slaves = srv.Slaves()
	require.Len(t, slaves, 1)
	assert.False(t, slaves[0].Stale)
	assert.Equal(t, []string{"greeter"}, slaves[0].Agents)
}

func TestStart_SlaveWithoutLeader(t *testing.T) {
	srv, err := New(Config{Mode: ModeSlave})
	require.NoError(t, err)
	assert.NotPanics(t, func() {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		_ = srv.Start(ctx)
	})
}
