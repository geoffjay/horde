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

// TestPutKBFile_ParticipantForward creates a two-node setup (master + slave)
// and verifies that a PUT on the slave forwards to the master, and a 412 is
// returned verbatim when the CAS precondition fails.
func TestPutKBFile_ParticipantForward(t *testing.T) {
	// Master: KB sync enabled, serves as the authority.
	masterWorkspace := t.TempDir()
	masterSrv, err := server.New(server.Config{
		Mode:              server.ModeMaster,
		SpawnDefaultAgent: false,
		KBSync: server.KBSyncConfig{
			Enabled:     true,
			MaxFileSize: 1048576,
		},
	})
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	require.NoError(t, masterSrv.Start(ctx))

	mp, err := masterSrv.CreateProjectForTest(server.CreateProjectInput{
		Name:       "test-proj",
		Workspace:  masterWorkspace,
		AgentNames: []string{"greeter"},
	})
	require.NoError(t, err)

	// Start an HTTP server for the master.
	masterRouter := Router(masterSrv)
	masterHS := httptest.NewServer(masterRouter)
	t.Cleanup(masterHS.Close)

	// Create a file on the master first, so we have a digest for If-Match.
	w := doRaw(t, masterRouter, http.MethodPut,
		"/api/v1/kb/project/"+mp.ID+"/file?path=existing.md",
		[]byte("# v1\n"),
		map[string]string{"If-None-Match": "*"})
	require.Equal(t, http.StatusOK, w.Code)
	etag := w.Header().Get("ETag")
	require.NotEmpty(t, etag)

	// Participant: KB sync enabled, slave mode with the master as leader.
	partSrv, err := server.New(server.Config{
		Mode:              server.ModeSlave,
		SpawnDefaultAgent: false,
		KBSync: server.KBSyncConfig{
			Enabled:     true,
			MaxFileSize: 1048576,
		},
		Leader: masterHS.Listener.Addr().String(),
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

	// PUT with correct If-Match should forward to master and succeed.
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
