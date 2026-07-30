//go:build integration

package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newKBPushIntegrationEnv sets up a master + participant pair where the
// participant has WatchLocal enabled (stage 2). The participant watches its
// local tree and pushes local edits to the authority.
func newKBPushIntegrationEnv(t *testing.T) *kbIntegrationEnv {
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
		Port:                0,
	})
	require.NoError(t, err)
	require.NoError(t, masterSrv.Start(ctx))

	p, err := masterSrv.CreateProjectForTest(CreateProjectInput{
		Name:       "test-proj",
		Workspace:  masterWorkspace,
		AgentNames: []string{"greeter"},
	})
	require.NoError(t, err)

	// Start an HTTP server on the master to serve KB routes (manifest, file,
	// and now PUT/DELETE for the push path).
	masterRouter := newKBOnlyRouter(masterSrv)
	masterHS := httptest.NewServer(masterRouter)
	t.Cleanup(masterHS.Close)

	// Participant: KB sync enabled + WatchLocal (stage 2).
	partStateDir := t.TempDir()
	partDataDir := t.TempDir()
	partSrv, err := New(Config{
		Mode:              ModeSlave,
		SpawnDefaultAgent: false,
		KBSync: KBSyncConfig{
			Enabled:       true,
			WatchLocal:    true,
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

	partSrv.leader = newLeaderClientForTest(masterHS.Listener.Addr().String(), "", "")

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

// TestKBPush_LocalEditPropagatesToAuthority verifies the core stage-2 behavior:
// editing a file directly on the participant's local tree propagates to the
// authority through the push path (KSP §5.1 push row, §11).
func TestKBPush_LocalEditPropagatesToAuthority(t *testing.T) {
	env := newKBPushIntegrationEnv(t)

	resolver := env.partSrv.KBResolveScope(kbScopeKindProject)
	require.NotNil(t, resolver)
	projects := env.partSrv.ListProjects("")
	require.Len(t, projects, 1)

	localTree, err := resolver.LocalTree(projects[0].ID)
	require.NoError(t, err)

	// First, let convergence pull the initial manifest from the authority so
	// the participant has synced_digests for all files. Wait for the
	// specific file's sync record, not just the manifest digest, to ensure
	// the pull has completed (the manifest digest is set before paths are
	// processed).
	deadline := time.After(5 * time.Second)
	for {
		syncStore := env.partSrv.kbSyncMgr.Get("project", projects[0].ID)
		if syncStore.Get("index.md") != "" {
			break
		}
		select {
		case <-deadline:
			t.Fatal("participant did not pull initial files within 5s")
		case <-time.After(50 * time.Millisecond):
		}
	}

	// Edit a file on the participant's local tree (a direct file edit, the
	// stage-2 use case). This is the file the authority scaffolded.
	filePath := filepath.Join(localTree, "index.md")
	original, err := os.ReadFile(filePath)
	require.NoError(t, err)
	require.NotEmpty(t, original)

	newContent := []byte("# Edited on participant\n")
	require.NoError(t, os.WriteFile(filePath, newContent, 0o644))

	// Wait for the watcher to trigger an early poll and the converger to push
	// the edit to the authority, then for the authority's manifest to reflect
	// the new content.
	deadline = time.After(10 * time.Second)
	for {
		// Check the authority's manifest for the new digest.
		manifest, err := env.masterSrv.KBResolveScope(kbScopeKindProject).
			CachedManifest(KBScopeRef{Kind: "project", ID: projects[0].ID})
		require.NoError(t, err)
		for _, e := range manifest.Files {
			if e.Path == "index.md" {
				if e.Digest == sha256Hex(newContent) {
					return // the edit propagated
				}
			}
		}
		select {
		case <-deadline:
			t.Fatal("participant edit did not propagate to authority within 10s")
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// TestKBPush_NewLocalFilePropagates verifies that a new file created on the
// participant's local tree is pushed to the authority (KSP §5.1 push-new row).
func TestKBPush_NewLocalFilePropagates(t *testing.T) {
	env := newKBPushIntegrationEnv(t)

	resolver := env.partSrv.KBResolveScope(kbScopeKindProject)
	projects := env.partSrv.ListProjects("")
	localTree, err := resolver.LocalTree(projects[0].ID)
	require.NoError(t, err)

	// Wait for initial convergence to pull files.
	deadline := time.After(5 * time.Second)
	for {
		syncStore := env.partSrv.kbSyncMgr.Get("project", projects[0].ID)
		if syncStore.Get("index.md") != "" {
			break
		}
		select {
		case <-deadline:
			t.Fatal("participant did not pull initial files within 5s")
		case <-time.After(50 * time.Millisecond):
		}
	}

	// Create a new file on the participant.
	newContent := []byte("# Brand new doc\n")
	require.NoError(t, os.WriteFile(filepath.Join(localTree, "new-doc.md"), newContent, 0o644))

	// Wait for it to appear on the authority.
	deadline = time.After(10 * time.Second)
	for {
		manifest, err := env.masterSrv.KBResolveScope(kbScopeKindProject).
			CachedManifest(KBScopeRef{Kind: "project", ID: projects[0].ID})
		require.NoError(t, err)
		for _, e := range manifest.Files {
			if e.Path == "new-doc.md" && e.Digest == sha256Hex(newContent) {
				return
			}
		}
		select {
		case <-deadline:
			t.Fatal("new participant file did not propagate to authority within 10s")
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// TestKBPush_ConcurrentEditProducesOneWinner verifies that a concurrent edit
// on the authority and the participant to the same file produces exactly one
// canonical winner and a preserved conflict copy on the loser (KSP §6).
func TestKBPush_ConcurrentEditProducesOneWinner(t *testing.T) {
	env := newKBPushIntegrationEnv(t)

	resolver := env.partSrv.KBResolveScope(kbScopeKindProject)
	projects := env.partSrv.ListProjects("")
	localTree, err := resolver.LocalTree(projects[0].ID)
	require.NoError(t, err)

	// Wait for initial convergence so we have a synced_digest for the file.
	deadline := time.After(5 * time.Second)
	var syncedDigest string
	for {
		syncStore := env.partSrv.kbSyncMgr.Get("project", projects[0].ID)
		syncedDigest = syncStore.Get("index.md")
		if syncedDigest != "" {
			break
		}
		select {
		case <-deadline:
			t.Fatal("participant did not converge initial manifest within 5s")
		case <-time.After(50 * time.Millisecond):
		}
	}

	// Edit the file on the authority directly (bypasses CAS — last write
	// to disk wins on the authority, KSP §6.2).
	masterResolver := env.masterSrv.KBResolveScope(kbScopeKindProject)
	masterTree, err := masterResolver.AuthorityTree(projects[0].ID)
	require.NoError(t, err)
	masterContent := []byte("# Authority wins\n")
	require.NoError(t, os.WriteFile(filepath.Join(masterTree, "index.md"), masterContent, 0o644))
	masterResolver.InvalidateCache(projects[0].ID)

	// Simultaneously edit the file on the participant. The converger will
	// try to push with If-Match: syncedDigest, but the authority's digest
	// has changed → 412. The participant should preserve its local content
	// to the conflict area, then converge to the authority's version.
	partContent := []byte("# Participant loses\n")
	require.NoError(t, os.WriteFile(filepath.Join(localTree, "index.md"), partContent, 0o644))

	// Wait for convergence: the participant's file should match the
	// authority's version (the winner), and a conflict copy should exist.
	deadline = time.After(10 * time.Second)
	for {
		data, err := os.ReadFile(filepath.Join(localTree, "index.md"))
		require.NoError(t, err)
		if string(data) == string(masterContent) {
			break // converged to the winner
		}
		select {
		case <-deadline:
			t.Fatal("participant did not converge to authority's version within 10s")
		case <-time.After(100 * time.Millisecond):
		}
	}

	// Verify a conflict copy was preserved.
	scope := KBScopeRef{Kind: "project", ID: projects[0].ID}
	conflicts, err := env.partSrv.kbConflict.List(scope)
	require.NoError(t, err)
	assert.NotEmpty(t, conflicts, "a conflict copy should have been preserved")
	if len(conflicts) > 0 {
		assert.Equal(t, "index.md", conflicts[0].Path,
			"conflict should be for the concurrently-edited file")
	}

	// Verify the conflict copy contains the participant's lost content.
	found := false
	for _, c := range conflicts {
		fullPath := filepath.Join(env.partSrv.kbConflict.dir, c.Scope.Kind, c.Scope.ID, c.File)
		data, err := os.ReadFile(fullPath)
		require.NoError(t, err)
		if string(data) == string(partContent) {
			found = true
			break
		}
	}
	assert.True(t, found, "the participant's lost content should be preserved in a conflict copy")
}

// TestKBPush_OfflineEditReplaysOnReconnect verifies that an edit made while
// the leader is unreachable stays on disk and is pushed when the leader
// comes back (KSP §11, stage 2: the three-way comparison + persisted sync
// records are the replay mechanism — D≠S paths are pushed on the next poll).
func TestKBPush_OfflineEditReplaysOnReconnect(t *testing.T) {
	// Use a toggleable HTTP server so we can simulate the leader going
	// down without replacing s.leader (which would race with the
	// convergence goroutine reading it).
	var offline atomic.Bool

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
		Port:                0,
	})
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	require.NoError(t, masterSrv.Start(ctx))

	p, err := masterSrv.CreateProjectForTest(CreateProjectInput{
		Name:       "test-proj",
		Workspace:  masterWorkspace,
		AgentNames: []string{"greeter"},
	})
	require.NoError(t, err)

	// Wrap the master's KB router in a toggleable handler.
	masterRouter := newKBOnlyRouter(masterSrv)
	toggleable := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if offline.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		masterRouter.ServeHTTP(w, r)
	})
	masterHS := httptest.NewServer(toggleable)
	t.Cleanup(masterHS.Close)

	// Participant: WatchLocal enabled (stage 2).
	partStateDir := t.TempDir()
	partDataDir := t.TempDir()
	partSrv, err := New(Config{
		Mode:              ModeSlave,
		SpawnDefaultAgent: false,
		KBSync: KBSyncConfig{
			Enabled:       true,
			WatchLocal:    true,
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
	partSrv.leader = newLeaderClientForTest(masterHS.Listener.Addr().String(), "", "")

	_, err = partSrv.CreateProjectForTest(CreateProjectInput{
		Name:       "test-proj",
		Workspace:  filepath.Join(partDataDir, "workspaces", "project", p.ID, ".horde"),
		AgentNames: []string{"greeter"},
	})
	require.NoError(t, err)
	require.NoError(t, partSrv.Start(ctx))

	resolver := partSrv.KBResolveScope(kbScopeKindProject)
	projects := partSrv.ListProjects("")
	localTree, err := resolver.LocalTree(projects[0].ID)
	require.NoError(t, err)

	// Wait for initial convergence to pull files.
	deadline := time.After(5 * time.Second)
	for {
		syncStore := partSrv.kbSyncMgr.Get("project", projects[0].ID)
		if syncStore.Get("index.md") != "" {
			break
		}
		select {
		case <-deadline:
			t.Fatal("participant did not pull initial files within 5s")
		case <-time.After(50 * time.Millisecond):
		}
	}

	// Simulate offline: the master returns 503 for all requests.
	offline.Store(true)

	// Edit the file while "offline".
	offlineContent := []byte("# Edited while offline\n")
	require.NoError(t, os.WriteFile(filepath.Join(localTree, "index.md"), offlineContent, 0o644))

	// Wait a bit to ensure the converger tries and fails (503 from the
	// master means the manifest fetch fails and the poll is skipped).
	time.Sleep(500 * time.Millisecond)

	// Verify the edit is still on disk (not lost).
	data, err := os.ReadFile(filepath.Join(localTree, "index.md"))
	require.NoError(t, err)
	assert.Equal(t, string(offlineContent), string(data))

	// Reconnect: the master is available again.
	offline.Store(false)

	// Wait for the converger to reconnect and push the edit.
	deadline = time.After(10 * time.Second)
	for {
		manifest, err := masterSrv.KBResolveScope(kbScopeKindProject).
			CachedManifest(KBScopeRef{Kind: "project", ID: projects[0].ID})
		require.NoError(t, err)
		for _, e := range manifest.Files {
			if e.Path == "index.md" && e.Digest == sha256Hex(offlineContent) {
				return // the offline edit propagated
			}
		}
		select {
		case <-deadline:
			t.Fatal("offline edit did not replay on reconnect within 10s")
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// TestKBPush_EditPropagatesToThirdNode verifies the full chain: an edit on a
// participant (stage 2) pushes to the authority, and the authority's updated
// manifest propagates the change to a second participant via normal pull
// convergence (KSP §11 — the plan's "edit on a participant propagating to the
// authority and on to a third node").
func TestKBPush_EditPropagatesToThirdNode(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	// Master (authority).
	masterWorkspace := t.TempDir()
	masterStateDir := t.TempDir()
	masterSrv, err := New(Config{
		Mode:                ModeMaster,
		SpawnDefaultAgent:   false,
		KBSync:              KBSyncConfig{Enabled: true, MaxFileSize: 1048576, Ignore: []string{"*.tmp"}},
		StateDir:            masterStateDir,
		ProjectWorkspaceDir: masterWorkspace,
		Port:                0,
	})
	require.NoError(t, err)
	require.NoError(t, masterSrv.Start(ctx))

	p, err := masterSrv.CreateProjectForTest(CreateProjectInput{
		Name: "test-proj", Workspace: masterWorkspace, AgentNames: []string{"greeter"},
	})
	require.NoError(t, err)

	masterRouter := newKBOnlyRouter(masterSrv)
	masterHS := httptest.NewServer(masterRouter)
	t.Cleanup(masterHS.Close)

	// Participant 1: WatchLocal enabled (pushes local edits).
	part1DataDir := t.TempDir()
	part1Srv, err := New(Config{
		Mode:              ModeSlave,
		SpawnDefaultAgent: false,
		KBSync: KBSyncConfig{
			Enabled: true, WatchLocal: true, MaxFileSize: 1048576, Ignore: []string{"*.tmp"},
			PollInterval:  200 * time.Millisecond,
			WorkspaceRoot: filepath.Join(part1DataDir, "workspaces"),
		},
		StateDir: t.TempDir(), DataDir: part1DataDir, Port: 0,
	})
	require.NoError(t, err)
	part1Srv.leader = newLeaderClientForTest(masterHS.Listener.Addr().String(), "", "")
	_, err = part1Srv.CreateProjectForTest(CreateProjectInput{
		Name:       "test-proj",
		Workspace:  filepath.Join(part1DataDir, "workspaces", "project", p.ID, ".horde"),
		AgentNames: []string{"greeter"},
	})
	require.NoError(t, err)
	require.NoError(t, part1Srv.Start(ctx))

	// Participant 2: read-only convergence (stage 1 — no WatchLocal).
	part2DataDir := t.TempDir()
	part2Srv, err := New(Config{
		Mode:              ModeSlave,
		SpawnDefaultAgent: false,
		KBSync: KBSyncConfig{
			Enabled: true, MaxFileSize: 1048576, Ignore: []string{"*.tmp"},
			PollInterval:  200 * time.Millisecond,
			WorkspaceRoot: filepath.Join(part2DataDir, "workspaces"),
		},
		StateDir: t.TempDir(), DataDir: part2DataDir, Port: 0,
	})
	require.NoError(t, err)
	part2Srv.leader = newLeaderClientForTest(masterHS.Listener.Addr().String(), "", "")
	_, err = part2Srv.CreateProjectForTest(CreateProjectInput{
		Name:       "test-proj",
		Workspace:  filepath.Join(part2DataDir, "workspaces", "project", p.ID, ".horde"),
		AgentNames: []string{"greeter"},
	})
	require.NoError(t, err)
	require.NoError(t, part2Srv.Start(ctx))

	// Wait for both participants to converge the initial manifest.
	for _, srv := range []*Server{part1Srv, part2Srv} {
		deadline := time.After(5 * time.Second)
		for {
			ss := srv.kbSyncMgr.Get("project", p.ID)
			if ss.Get("index.md") != "" {
				break
			}
			select {
			case <-deadline:
				t.Fatal("participant did not pull initial files within 5s")
			case <-time.After(50 * time.Millisecond):
			}
		}
	}

	// Edit a file on participant 1 (the pusher).
	resolver1 := part1Srv.KBResolveScope(kbScopeKindProject)
	localTree1, err := resolver1.LocalTree(p.ID)
	require.NoError(t, err)
	newContent := []byte("# Edit on participant 1\n")
	require.NoError(t, os.WriteFile(filepath.Join(localTree1, "index.md"), newContent, 0o644))

	// Wait for the edit to appear on participant 2 (pulled from the authority
	// after participant 1 pushed it).
	resolver2 := part2Srv.KBResolveScope(kbScopeKindProject)
	localTree2, err := resolver2.LocalTree(p.ID)
	require.NoError(t, err)
	deadline := time.After(15 * time.Second)
	for {
		data, err := os.ReadFile(filepath.Join(localTree2, "index.md"))
		require.NoError(t, err)
		if string(data) == string(newContent) {
			return // propagated through the authority to the third node
		}
		select {
		case <-deadline:
			t.Fatal("edit did not propagate to third node within 15s")
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// TestKBConvergence_ParticipantLearnsProjectFromLeader verifies that a
// participant whose local project store is empty (the real-slave case —
// project API requests forward to the master) still converges: the converger
// fetches the project list from the leader and materializes the local tree
// (KSP §3.2: participants learn projects from the authority).
func TestKBConvergence_ParticipantLearnsProjectFromLeader(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	// Master (authority) with a scaffolded project.
	masterWorkspace := t.TempDir()
	masterSrv, err := New(Config{
		Mode:                ModeMaster,
		SpawnDefaultAgent:   false,
		KBSync:              KBSyncConfig{Enabled: true, MaxFileSize: 1048576, Ignore: []string{"*.tmp"}},
		StateDir:            t.TempDir(),
		ProjectWorkspaceDir: masterWorkspace,
		Port:                0,
	})
	require.NoError(t, err)
	require.NoError(t, masterSrv.Start(ctx))

	p, err := masterSrv.CreateProjectForTest(CreateProjectInput{
		Name: "test-proj", Workspace: masterWorkspace, AgentNames: []string{"greeter"},
	})
	require.NoError(t, err)

	masterHS := httptest.NewServer(newKBOnlyRouter(masterSrv))
	t.Cleanup(masterHS.Close)

	// Participant: KB sync enabled, but the project is NOT created locally —
	// it must be learned from the leader (the real-slave scenario).
	partDataDir := t.TempDir()
	partSrv, err := New(Config{
		Mode:              ModeSlave,
		SpawnDefaultAgent: false,
		KBSync: KBSyncConfig{
			Enabled: true, MaxFileSize: 1048576, Ignore: []string{"*.tmp"},
			PollInterval:  200 * time.Millisecond,
			WorkspaceRoot: filepath.Join(partDataDir, "workspaces"),
		},
		StateDir: t.TempDir(), DataDir: partDataDir, Port: 0,
	})
	require.NoError(t, err)
	partSrv.leader = newLeaderClientForTest(masterHS.Listener.Addr().String(), "", "")
	require.NoError(t, partSrv.Start(ctx))

	// The participant's local store is empty.
	require.Empty(t, partSrv.ListProjects(""), "participant local store should be empty")

	// Convergence should still materialize the project's KB tree, learned
	// from the leader's project list.
	resolver := partSrv.KBResolveScope(kbScopeKindProject)
	localTree, err := resolver.LocalTree(p.ID)
	require.NoError(t, err)
	deadline := time.After(10 * time.Second)
	for {
		if data, rerr := os.ReadFile(filepath.Join(localTree, "index.md")); rerr == nil && len(data) > 0 {
			return // the participant materialized the tree from the leader's project list
		}
		select {
		case <-deadline:
			t.Fatal("participant did not learn project from leader and materialize within 10s")
		case <-time.After(100 * time.Millisecond):
		}
	}
}
