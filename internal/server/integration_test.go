package server_test

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/geoffjay/horde/internal/api"
	"github.com/geoffjay/horde/internal/server"
)

// TestSlaveRegistersWithRealMasterAPI wires a real slave leader-client (via
// connectLeader) against the real internal/api router backed by a master
// Server. It is the seam that catches drift between the hand-mirrored
// register/heartbeat request+response structs in internal/api and
// internal/server: if a JSON tag diverges, register or heartbeat fails here.
func TestSlaveRegistersWithRealMasterAPI(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	master, err := server.New(server.Config{
		Mode:              server.ModeMaster,
		NodeID:            "master-1",
		SpawnDefaultAgent: false,
	})
	require.NoError(t, err)
	require.NoError(t, master.Start(ctx))

	ts := httptest.NewServer(api.Router(master, master.EventBus()))
	defer ts.Close()

	slave, err := server.New(server.Config{
		Mode:              server.ModeSlave,
		Leader:            ts.URL, // the leader client strips the scheme
		NodeID:            "slave-1",
		SpawnDefaultAgent: false,
	})
	require.NoError(t, err)
	require.NoError(t, slave.Start(ctx))

	// register succeeds over the real API → the slave reports connected.
	require.Eventually(t, slave.LeaderConnected, 5*time.Second, 20*time.Millisecond)

	// The master's cluster view reflects the slave via the real register/
	// heartbeat round trip.
	require.Eventually(t, func() bool {
		for _, s := range master.Slaves() {
			if s.NodeID == "slave-1" {
				return true
			}
		}
		return false
	}, 5*time.Second, 20*time.Millisecond)

	found := false
	for _, s := range master.Slaves() {
		if s.NodeID == "slave-1" {
			found = true
			assert.False(t, s.Stale, "freshly registered slave should not be stale")
		}
	}
	require.True(t, found)
}
