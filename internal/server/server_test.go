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

func TestStart_SlaveBecomesLeaderConnected(t *testing.T) {
	srv, err := New(Config{Mode: ModeSlave, Leader: "master:13420"})
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	require.NoError(t, srv.Start(ctx))
	defer cancel()

	// connectLeader runs in a goroutine and marks leaderOK within a tick.
	require.Eventually(t, srv.LeaderConnected, time.Second, 20*time.Millisecond)
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
