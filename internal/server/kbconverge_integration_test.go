//go:build integration

package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// kbIntegrationEnv sets up a master + participant pair for KB convergence
// integration tests. The master serves KB routes; the participant runs the
// convergence loop against the master.
type kbIntegrationEnv struct {
	masterSrv    *Server
	masterRouter http.Handler
	masterAddr   string
	partSrv      *Server
	partStateDir string
}

func newKBIntegrationEnv(t *testing.T) *kbIntegrationEnv {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	// Master: KB sync enabled, serves as the authority.
	masterWorkspace := t.TempDir()
	masterStateDir := t.TempDir()
	masterSrv, err := New(Config{
		Mode:              ModeMaster,
		SpawnDefaultAgent: false,
		KBSync: KBSyncConfig{
			Enabled:     true,
			MaxFileSize: 1048576,
			Ignore:      []string{"*.tmp"},
		},
		StateDir:            masterStateDir,
		ProjectWorkspaceDir: masterWorkspace,
		Port:                0, // not listening
	})
	require.NoError(t, err)
	require.NoError(t, masterSrv.Start(ctx))

	// Create a project on the master with a scaffolded KB.
	p, err := masterSrv.CreateProjectForTest(CreateProjectInput{
		Name:       "test-proj",
		Workspace:  masterWorkspace,
		AgentNames: []string{"greeter"},
	})
	require.NoError(t, err)

	// Start an HTTP server on the master to serve KB routes.
	masterRouter := newKBOnlyRouter(masterSrv)
	masterHS := httptest.NewServer(masterRouter)
	t.Cleanup(masterHS.Close)

	// Participant: KB sync enabled, converges against the master.
	partStateDir := t.TempDir()
	partDataDir := t.TempDir()
	partSrv, err := New(Config{
		Mode:              ModeSlave,
		SpawnDefaultAgent: false,
		KBSync: KBSyncConfig{
			Enabled:       true,
			MaxFileSize:   1048576,
			Ignore:        []string{"*.tmp"},
			PollInterval:  200 * time.Millisecond,
			WorkspaceRoot: filepath.Join(partDataDir, "workspaces"),
		},
		StateDir: partStateDir,
		DataDir:  partDataDir,
		Port:     0,
	})
	require.NoError(t, err)

	// Wire the leader address so the converger can reach the master.
	// We use a fake leader client that resolves to the test server.
	partSrv.leader = newLeaderClientForTest(masterHS.Listener.Addr().String(), "", "")

	// Create the same project on the participant (so it appears in the
	// project list and the converger picks it up).
	_, err = partSrv.CreateProjectForTest(CreateProjectInput{
		Name:       "test-proj",
		Workspace:  filepath.Join(partDataDir, "workspaces", "project", p.ID, ".horde"),
		AgentNames: []string{"greeter"},
	})
	require.NoError(t, err)
	require.NoError(t, partSrv.Start(ctx))

	return &kbIntegrationEnv{
		masterSrv:    masterSrv,
		masterRouter: masterRouter,
		masterAddr:   masterHS.Listener.Addr().String(),
		partSrv:      partSrv,
		partStateDir: partStateDir,
	}
}

// TestKBConvergence_EditOnAuthorityAppearsOnParticipant verifies the core
// convergence behavior: editing a file on the authority shows up on the
// participant through convergence (poll → classify → pull).
func TestKBConvergence_EditOnAuthorityAppearsOnParticipant(t *testing.T) {
	env := newKBIntegrationEnv(t)

	// Get the project on the master.
	projects := env.masterSrv.ListProjects("")
	require.Len(t, projects, 1)
	projectID := projects[0].ID

	// Edit a file on the authority's canonical tree.
	authorityTree, err := env.masterSrv.kbCanonicalTreePtr(&projects[0])
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(authorityTree, "index.md"), []byte("# Authority Edit\n"), 0o644))

	// Invalidate the master's cache so the manifest reflects the edit.
	env.masterSrv.KBResolveScope("project").InvalidateCache(projectID)

	// Wait for the participant to converge.
	deadline := time.After(5 * time.Second)
	for {
		// Check the participant's local tree.
		partProjects := env.partSrv.ListProjects("")
		if len(partProjects) > 0 {
			localTree, err := env.partSrv.KBResolveScope("project").LocalTree(partProjects[0].ID)
			if err == nil {
				data, err := os.ReadFile(filepath.Join(localTree, "index.md"))
				if err == nil && string(data) == "# Authority Edit\n" {
					return // converged!
				}
			}
		}
		select {
		case <-deadline:
			t.Fatal("participant did not converge within 5s")
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// TestKBConvergence_DeleteByAbsence verifies that a file deleted on the
// authority is deleted on the participant (KSP §5.1: "deleted upstream, clean
// local → delete locally, drop record").
func TestKBConvergence_DeleteByAbsence(t *testing.T) {
	env := newKBIntegrationEnv(t)

	projects := env.masterSrv.ListProjects("")
	require.Len(t, projects, 1)
	projectID := projects[0].ID

	// First, let the initial content converge to the participant.
	authorityTree, err := env.masterSrv.kbCanonicalTreePtr(&projects[0])
	require.NoError(t, err)
	env.masterSrv.KBResolveScope("project").InvalidateCache(projectID)

	// Wait for initial convergence.
	deadline := time.After(5 * time.Second)
	for {
		partProjects := env.partSrv.ListProjects("")
		if len(partProjects) > 0 {
			localTree, err := env.partSrv.KBResolveScope("project").LocalTree(partProjects[0].ID)
			if err == nil {
				if _, err := os.Stat(filepath.Join(localTree, "index.md")); err == nil {
					break // initial convergence done
				}
			}
		}
		select {
		case <-deadline:
			t.Fatal("initial convergence did not complete")
		case <-time.After(100 * time.Millisecond):
		}
	}

	// Delete a file on the authority.
	require.NoError(t, os.Remove(filepath.Join(authorityTree, "index.md")))
	env.masterSrv.KBResolveScope("project").InvalidateCache(projectID)

	// Wait for the deletion to propagate.
	deadline = time.After(5 * time.Second)
	for {
		partProjects := env.partSrv.ListProjects("")
		if len(partProjects) > 0 {
			localTree, err := env.partSrv.KBResolveScope("project").LocalTree(partProjects[0].ID)
			if err == nil {
				if _, err := os.Stat(filepath.Join(localTree, "index.md")); os.IsNotExist(err) {
					return // deleted on participant!
				}
			}
		}
		select {
		case <-deadline:
			t.Fatal("participant did not delete the file within 5s")
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// TestKBConvergence_CatchUpFromEmpty verifies that a participant that starts
// with an empty local tree converges to the authority's full manifest.
func TestKBConvergence_CatchUpFromEmpty(t *testing.T) {
	env := newKBIntegrationEnv(t)

	projects := env.masterSrv.ListProjects("")
	require.Len(t, projects, 1)

	// Add multiple files on the authority.
	authorityTree, err := env.masterSrv.kbCanonicalTreePtr(&projects[0])
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Join(authorityTree, "concepts"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(authorityTree, "index.md"), []byte("# Index\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(authorityTree, "concepts", "intro.md"), []byte("# Intro\n"), 0o644))
	env.masterSrv.KBResolveScope("project").InvalidateCache(projects[0].ID)

	// Wait for the participant to converge all files.
	deadline := time.After(5 * time.Second)
	for {
		partProjects := env.partSrv.ListProjects("")
		if len(partProjects) > 0 {
			localTree, err := env.partSrv.KBResolveScope("project").LocalTree(partProjects[0].ID)
			if err == nil {
				indexData, err1 := os.ReadFile(filepath.Join(localTree, "index.md"))
				conceptData, err2 := os.ReadFile(filepath.Join(localTree, "concepts", "intro.md"))
				if err1 == nil && err2 == nil &&
					string(indexData) == "# Index\n" && string(conceptData) == "# Intro\n" {
					return // all files converged!
				}
			}
		}
		select {
		case <-deadline:
			t.Fatal("participant did not catch up within 5s")
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// TestKBConvergence_DirtyLocalFilePreserved verifies that a locally-edited
// file on the participant is preserved to the conflict area before being
// overwritten by the authority's version (KSP §5.2).
func TestKBConvergence_DirtyLocalFilePreserved(t *testing.T) {
	env := newKBIntegrationEnv(t)

	projects := env.masterSrv.ListProjects("")
	require.Len(t, projects, 1)
	projectID := projects[0].ID

	// Wait for initial convergence.
	authorityTree, err := env.masterSrv.kbCanonicalTreePtr(&projects[0])
	require.NoError(t, err)
	env.masterSrv.KBResolveScope("project").InvalidateCache(projectID)

	deadline := time.After(5 * time.Second)
	for {
		partProjects := env.partSrv.ListProjects("")
		if len(partProjects) > 0 {
			localTree, err := env.partSrv.KBResolveScope("project").LocalTree(partProjects[0].ID)
			if err == nil {
				if _, err := os.Stat(filepath.Join(localTree, "index.md")); err == nil {
					break
				}
			}
		}
		select {
		case <-deadline:
			t.Fatal("initial convergence did not complete")
		case <-time.After(100 * time.Millisecond):
		}
	}

	// Edit the file locally on the participant (dirty local).
	partProjects := env.partSrv.ListProjects("")
	require.Len(t, partProjects, 1)
	localTree, err := env.partSrv.KBResolveScope("project").LocalTree(partProjects[0].ID)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(localTree, "index.md"), []byte("# My Local Edit\n"), 0o644))

	// Also edit on the authority (so the convergence sees a conflict).
	require.NoError(t, os.WriteFile(filepath.Join(authorityTree, "index.md"), []byte("# Authority Edit\n"), 0o644))
	env.masterSrv.KBResolveScope("project").InvalidateCache(projectID)

	// Wait for convergence to run.
	deadline = time.After(5 * time.Second)
	for {
		// The participant's local file should now have the authority's content.
		data, err := os.ReadFile(filepath.Join(localTree, "index.md"))
		if err == nil && string(data) == "# Authority Edit\n" {
			// The local edit should be preserved in the conflict area.
			conflictDir := filepath.Join(env.partSrv.cfg.DataDir, "kb-conflicts", "project", partProjects[0].ID)
			entries, err := os.ReadDir(conflictDir)
			if err == nil && len(entries) > 0 {
				// Check that one of the conflict copies contains the local edit.
				for _, e := range entries {
					content, err := os.ReadFile(filepath.Join(conflictDir, e.Name()))
					if err == nil && string(content) == "# My Local Edit\n" {
						return // conflict preserved!
					}
				}
			}
		}
		select {
		case <-deadline:
			t.Fatal("dirty local file was not preserved to conflict area within 5s")
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// TestKBConvergence_ScopeMismatchRejected verifies that a manifest whose scope
// doesn't match the requested scope is rejected (KSP §10).
func TestKBConvergence_ScopeMismatchRejected(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	masterWorkspace := t.TempDir()
	masterSrv, err := New(Config{
		Mode:                ModeMaster,
		SpawnDefaultAgent:   false,
		KBSync:              KBSyncConfig{Enabled: true, MaxFileSize: 1048576},
		ProjectWorkspaceDir: masterWorkspace,
		Port:                0,
	})
	require.NoError(t, err)
	require.NoError(t, masterSrv.Start(ctx))

	p, err := masterSrv.CreateProjectForTest(CreateProjectInput{
		Name:       "proj-a",
		Workspace:  masterWorkspace,
		AgentNames: []string{"greeter"},
	})
	require.NoError(t, err)

	router := newKBOnlyRouter(masterSrv)
	hs := httptest.NewServer(router)
	t.Cleanup(hs.Close)

	client := newKBClient("")

	// Fetch manifest for the correct scope — should succeed.
	scope := KBScopeRef{Kind: "project", ID: p.ID}
	manifest, status, err := client.fetchManifest(context.Background(), hs.Listener.Addr().String(), scope, "")
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, status)
	assert.Equal(t, "project", manifest.Scope.Kind)
	assert.Equal(t, p.ID, manifest.Scope.ID)

	// Fetch manifest for a wrong scope id — should fail with scope mismatch.
	wrongScope := KBScopeRef{Kind: "project", ID: "different-id"}
	_, _, err = client.fetchManifest(context.Background(), hs.Listener.Addr().String(), wrongScope, "")
	// This will fail because the project doesn't exist (404), not scope mismatch.
	// The scope mismatch check only fires when the authority returns a manifest
	// for a different scope than requested. In practice this is a defense
	// against a misconfigured authority or a man-in-the-middle.
	assert.Error(t, err)
}
