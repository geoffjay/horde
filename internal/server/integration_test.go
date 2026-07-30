//go:build integration

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

// TestWorkerRegistersWithRealCoordinatorAPI wires a real worker leader-client (via
// connectLeader) against the real internal/api router backed by a coordinator
// Server. It is the seam that catches drift between the hand-mirrored
// register/heartbeat request+response structs in internal/api and
// internal/server: if a JSON tag diverges, register or heartbeat fails here.
func TestWorkerRegistersWithRealCoordinatorAPI(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	coordinator, err := server.New(server.Config{
		Mode:              server.ModeCoordinator,
		NodeID:            "coordinator-1",
		SpawnDefaultAgent: false,
	})
	require.NoError(t, err)
	require.NoError(t, coordinator.Start(ctx))

	ts := httptest.NewServer(api.Router(coordinator))
	defer ts.Close()

	worker, err := server.New(server.Config{
		Mode:              server.ModeWorker,
		Leader:            ts.URL, // the leader client strips the scheme
		NodeID:            "worker-1",
		SpawnDefaultAgent: false,
	})
	require.NoError(t, err)
	require.NoError(t, worker.Start(ctx))

	// register succeeds over the real API → the worker reports connected.
	require.Eventually(t, worker.LeaderConnected, 5*time.Second, 20*time.Millisecond)

	// The coordinator's cluster view reflects the worker via the real register/
	// heartbeat round trip.
	require.Eventually(t, func() bool {
		for _, s := range coordinator.Workers() {
			if s.NodeID == "worker-1" {
				return true
			}
		}
		return false
	}, 5*time.Second, 20*time.Millisecond)

	found := false
	for _, s := range coordinator.Workers() {
		if s.NodeID == "worker-1" {
			found = true
			assert.False(t, s.Stale, "freshly registered worker should not be stale")
		}
	}
	require.True(t, found)
}
