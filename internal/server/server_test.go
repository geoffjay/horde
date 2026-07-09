package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
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

func TestStart_SlaveBecomesLeaderConnected(t *testing.T) {
	// Stand up a fake master that accepts register + heartbeat so the real
	// leader client in connectLeader succeeds. This replaces the old test
	// which passed only because connectLeader faked leaderOK = true.
	var heartbeats atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/cluster/register", func(w http.ResponseWriter, r *http.Request) {
		var req registerPayload
		_ = json.NewDecoder(r.Body).Decode(&req)
		_ = json.NewEncoder(w).Encode(registerResponse{OK: true, NodeID: req.NodeID, LeaderID: "master"})
	})
	mux.HandleFunc("/api/v1/cluster/heartbeat", func(w http.ResponseWriter, r *http.Request) {
		heartbeats.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "leader_id": "master"})
	})
	master := httptest.NewServer(mux)
	defer master.Close()

	srv, err := New(Config{
		Mode:   ModeSlave,
		Leader: master.Listener.Addr().String(),
		NodeID: "slave-test",
	})
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	require.NoError(t, srv.Start(ctx))

	// connectLeader runs in a goroutine; once registered leaderOK flips.
	require.Eventually(t, srv.LeaderConnected, 5*time.Second, 20*time.Millisecond)

	// Wait for at least one heartbeat to confirm the loop is alive.
	require.Eventually(t, func() bool { return heartbeats.Load() > 0 }, 10*time.Second, 50*time.Millisecond)
}

func TestHeartbeat_BeforeRegister(t *testing.T) {
	// A master may receive a heartbeat before any register — e.g. after a
	// restart while a slave still believes it is connected. This must not
	// panic on the (previously nil) slaves map.
	srv, err := New(Config{Mode: ModeMaster, NodeID: "master-1"})
	require.NoError(t, err)

	assert.NotPanics(t, func() {
		leaderID, ok := srv.Heartbeat("slave-x")
		assert.True(t, ok)
		assert.Equal(t, "master-1", leaderID)
	})
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

func TestRun_StopsOnCancel(t *testing.T) {
	srv, err := New(Config{})
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx) }()

	cancel()
	select {
	case err := <-done:
		assert.ErrorIs(t, err, context.Canceled)
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
}
