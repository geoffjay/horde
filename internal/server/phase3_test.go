package server_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/geoffjay/horde/internal/api"
	"github.com/geoffjay/horde/internal/server"
)

// TestSpawnAgent_ReadyHandshakeAndInvoke verifies the full Phase 3 path:
// SpawnAgent starts a real agent subprocess, reads the spawn_ready handshake,
// records the socket path, and the node API reverse-proxies an invocation
// SSE stream through to the client.
//
// This test requires the real binary (built via `task build`) because it
// spawns the `horde agent` subprocess. It is skipped if the binary is not
// found.
func TestSpawnAgent_ReadyHandshakeAndInvoke(t *testing.T) {
	exe := findHordeBinary(t)

	srv, err := server.New(server.Config{
		AgentCommand:       exe,
		SocketDir:          "/tmp",
		ReadyTimeout:       10 * time.Second,
		HealthPollInterval: 0, // disable polling for the test
		SpawnDefaultAgent:  false,
	})
	require.NoError(t, err)
	require.NoError(t, srv.Start(context.Background()))
	t.Cleanup(func() {
		for _, a := range srv.Agents() {
			_ = srv.StopAgent(a.ID)
		}
	})

	// Spawn the greeter agent.
	id, err := srv.SpawnAgent(context.Background(), "greeter")
	require.NoError(t, err)
	assert.NotEmpty(t, id)

	// The socket path should be populated.
	require.Eventually(t, func() bool {
		return srv.AgentSocket(id) != ""
	}, 5*time.Second, 10*time.Millisecond)

	socketPath := srv.AgentSocket(id)
	require.NotEmpty(t, socketPath)
	assert.FileExists(t, socketPath)

	// Wire the node API and test it end-to-end.
	h := api.Router(srv)
	ts := httptest.NewServer(h)
	defer ts.Close()

	// POST /api/v1/agents/{id}/invoke — should reverse-proxy the SSE
	// stream from the agent subprocess.
	resp, err := http.Post(
		ts.URL+"/api/v1/agents/"+id+"/invoke",
		"application/json",
		strings.NewReader(`{"message":"hello"}`),
	)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	bodyStr := string(body)
	assert.Contains(t, bodyStr, "event: invocation")
	assert.Contains(t, bodyStr, "event: token")
	assert.Contains(t, bodyStr, "event: done")
	assert.Contains(t, bodyStr, "Hello from horde! You said: hello")
}

// TestSpawnAgent_SocketCleanup verifies that the socket file is removed when
// an agent is stopped.
func TestSpawnAgent_SocketCleanup(t *testing.T) {
	exe := findHordeBinary(t)

	srv, err := server.New(server.Config{
		AgentCommand:       exe,
		SocketDir:          "/tmp",
		ReadyTimeout:       10 * time.Second,
		HealthPollInterval: 0,
		SpawnDefaultAgent:  false,
	})
	require.NoError(t, err)
	require.NoError(t, srv.Start(context.Background()))

	id, err := srv.SpawnAgent(context.Background(), "greeter")
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		return srv.AgentSocket(id) != ""
	}, 5*time.Second, 10*time.Millisecond)

	socketPath := srv.AgentSocket(id)
	require.FileExists(t, socketPath)

	// Stop the agent — the socket file should be removed.
	require.NoError(t, srv.StopAgent(id))

	require.Eventually(t, func() bool {
		_, err := os.Stat(socketPath)
		return os.IsNotExist(err)
	}, 5*time.Second, 50*time.Millisecond, "socket file should be removed after stop")
}

// TestSpawnAgent_UnknownAgentFails verifies that spawning an agent not in
// the registry fails before any subprocess is started.
func TestSpawnAgent_UnknownAgentFails(t *testing.T) {
	srv, err := server.New(server.Config{
		SpawnDefaultAgent: false,
	})
	require.NoError(t, err)
	require.NoError(t, srv.Start(context.Background()))

	_, err = srv.SpawnAgent(context.Background(), "nonexistent-agent")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown agent")
}

// findHordeBinary returns the path to the built horde binary. The test is
// skipped if the binary does not exist (e.g. when running without `task build`).
func findHordeBinary(t *testing.T) string {
	t.Helper()
	// Try the Taskfile build output first.
	candidates := []string{
		filepath.Join("..", "..", "bin", "horde"),
	}
	// Also try os.Executable() — works if the test binary is the horde
	// binary (it is not, but this is a fallback for direct invocation).
	if exe, err := os.Executable(); err == nil {
		candidates = append(candidates, exe)
	}
	for _, c := range candidates {
		abs, err := filepath.Abs(c)
		if err != nil {
			continue
		}
		if info, err := os.Stat(abs); err == nil && !info.IsDir() {
			return abs
		}
	}
	t.Skip("horde binary not found — run `task build` before running subprocess tests")
	return ""
}
