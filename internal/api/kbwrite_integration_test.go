//go:build integration

package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/geoffjay/horde/internal/server"
)

// TestPutKBFile_ParticipantForward creates a two-node setup (coordinator + worker)
// and verifies that a PUT on the worker forwards to the coordinator, and a 412 is
// returned verbatim when the CAS precondition fails.
func TestPutKBFile_ParticipantForward(t *testing.T) {
	// Coordinator: KB sync enabled, serves as the authority.
	coordinatorWorkspace := t.TempDir()
	coordinatorSrv, err := server.New(server.Config{
		Mode:              server.ModeCoordinator,
		SpawnDefaultAgent: false,
		KBSync: server.KBSyncConfig{
			Enabled:     true,
			MaxFileSize: 1048576,
		},
	})
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	require.NoError(t, coordinatorSrv.Start(ctx))

	mp, err := coordinatorSrv.CreateProjectForTest(server.CreateProjectInput{
		Name:       "test-proj",
		Workspace:  coordinatorWorkspace,
		AgentNames: []string{"greeter"},
	})
	require.NoError(t, err)

	// Start an HTTP server for the coordinator.
	coordinatorRouter := Router(coordinatorSrv)
	coordinatorHS := httptest.NewServer(coordinatorRouter)
	t.Cleanup(coordinatorHS.Close)

	// Create a file on the coordinator first, so we have a digest for If-Match.
	w := doRaw(t, coordinatorRouter, http.MethodPut,
		"/api/v1/kb/project/"+mp.ID+"/file?path=existing.md",
		[]byte("# v1\n"),
		map[string]string{"If-None-Match": "*"})
	require.Equal(t, http.StatusOK, w.Code)
	etag := w.Header().Get("ETag")
	require.NotEmpty(t, etag)

	// Participant: KB sync enabled, worker mode with the coordinator as leader.
	partSrv, err := server.New(server.Config{
		Mode:              server.ModeWorker,
		SpawnDefaultAgent: false,
		KBSync: server.KBSyncConfig{
			Enabled:     true,
			MaxFileSize: 1048576,
		},
		Leader: coordinatorHS.Listener.Addr().String(),
	})
	require.NoError(t, err)
	require.NoError(t, partSrv.Start(ctx))

	// Create the same project on the participant (so it has a local scope).
	_, err = partSrv.CreateProjectForTest(server.CreateProjectInput{
		Name:       "test-proj",
		Workspace:  t.TempDir(),
		AgentNames: []string{"greeter"},
	})
	require.NoError(t, err)

	partRouter := Router(partSrv)

	// PUT with correct If-Match should forward to coordinator and succeed.
	w2 := doRaw(t, partRouter, http.MethodPut,
		"/api/v1/kb/project/"+mp.ID+"/file?path=existing.md",
		[]byte("# v2\n"),
		map[string]string{"If-Match": strings.Trim(etag, "\"")})
	assert.Equal(t, http.StatusOK, w2.Code, "forwarded PUT with correct If-Match should succeed")

	// PUT with wrong If-Match should forward and return 412 verbatim.
	w3 := doRaw(t, partRouter, http.MethodPut,
		"/api/v1/kb/project/"+mp.ID+"/file?path=existing.md",
		[]byte("# v3\n"),
		map[string]string{"If-Match": "sha256:wrong"})
	assert.Equal(t, http.StatusPreconditionFailed, w3.Code, "forwarded PUT with wrong If-Match should 412")
}
