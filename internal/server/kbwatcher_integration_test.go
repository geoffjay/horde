//go:build integration

package server

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestKBWatcher_RealEditInvalidatesCache verifies that a real fsnotify watcher
// observes a file edit on the canonical tree and invalidates the manifest cache,
// so the next manifest scan reflects the change without a manual InvalidateCache
// call (slice 2's core deliverable).
func TestKBWatcher_RealEditInvalidatesCache(t *testing.T) {
	tmp := t.TempDir()
	kbRoot := filepath.Join(tmp, ".horde", "knowledgebase")
	require.NoError(t, os.MkdirAll(kbRoot, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(kbRoot, "index.md"), []byte("# Original\n"), 0o644))

	cache := newKBManifestCache()
	policy := KBScopePolicy{MaxFileSize: DefaultKBMaxFileSize, Ignore: kbIgnoreGlobs()}
	scope := KBScopeRef{Kind: "project", ID: "p-test"}

	// Initial scan — populates the cache.
	manifest1, err := cache.cachedScan(kbRoot, policy, "node-a", scope)
	require.NoError(t, err)
	digest1 := manifest1.ManifestDigest

	// Start the watcher with a short debounce for the test.
	w, err := newKBWatcher(cache, 50*time.Millisecond)
	require.NoError(t, err)
	t.Cleanup(func() { _ = w.fsw.Close() })

	w.addTree(kbRoot)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go w.run(ctx)

	// Edit the file — the watcher should observe this and invalidate the cache.
	require.NoError(t, os.WriteFile(filepath.Join(kbRoot, "index.md"), []byte("# Edited\n"), 0o644))

	// Wait for the debounce window + fsnotify delivery latency.
	// On macOS kqueue, events can take up to ~100ms to arrive after a write.
	deadline := time.After(2 * time.Second)
	for {
		manifest2, err := cache.cachedScan(kbRoot, policy, "node-a", scope)
		require.NoError(t, err)
		if manifest2.ManifestDigest != digest1 {
			// Cache was invalidated and re-scanned — the edit is visible.
			var found bool
			for _, e := range manifest2.Files {
				if e.Path == "index.md" {
					found = true
				}
			}
			assert.True(t, found, "index.md should be in manifest after edit")
			return
		}
		select {
		case <-deadline:
			t.Fatal("watcher did not invalidate cache within 2s of edit")
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// TestKBWatcher_NewFileAppearsInManifest verifies that the watcher picks up a
// newly created file (not just edits to existing files).
func TestKBWatcher_NewFileAppearsInManifest(t *testing.T) {
	tmp := t.TempDir()
	kbRoot := filepath.Join(tmp, ".horde", "knowledgebase")
	require.NoError(t, os.MkdirAll(kbRoot, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(kbRoot, "index.md"), []byte("# Index\n"), 0o644))

	cache := newKBManifestCache()
	policy := KBScopePolicy{MaxFileSize: DefaultKBMaxFileSize, Ignore: kbIgnoreGlobs()}
	scope := KBScopeRef{Kind: "project", ID: "p-test"}

	manifest1, err := cache.cachedScan(kbRoot, policy, "node-a", scope)
	require.NoError(t, err)
	assert.Len(t, manifest1.Files, 1)

	w, err := newKBWatcher(cache, 50*time.Millisecond)
	require.NoError(t, err)
	t.Cleanup(func() { _ = w.fsw.Close() })

	w.addTree(kbRoot)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go w.run(ctx)

	// Create a new file in the tree.
	require.NoError(t, os.WriteFile(filepath.Join(kbRoot, "new.md"), []byte("# New\n"), 0o644))

	deadline := time.After(2 * time.Second)
	for {
		manifest2, err := cache.cachedScan(kbRoot, policy, "node-a", scope)
		require.NoError(t, err)
		if len(manifest2.Files) == 2 {
			assert.NotEqual(t, manifest1.ManifestDigest, manifest2.ManifestDigest)
			return
		}
		select {
		case <-deadline:
			t.Fatal("watcher did not pick up new file within 2s")
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// TestKBWatcher_EditorWriteTempRename verifies the debounce coalesces the
// write-temp-rename pattern editors use: the editor writes a temp file, then
// renames it over the target, producing two events within milliseconds. The
// cache should be invalidated exactly once (the debounce coalesces both events).
func TestKBWatcher_EditorWriteTempRename(t *testing.T) {
	tmp := t.TempDir()
	kbRoot := filepath.Join(tmp, ".horde", "knowledgebase")
	require.NoError(t, os.MkdirAll(kbRoot, 0o755))
	target := filepath.Join(kbRoot, "doc.md")
	require.NoError(t, os.WriteFile(target, []byte("# Original\n"), 0o644))

	cache := newKBManifestCache()
	policy := KBScopePolicy{MaxFileSize: DefaultKBMaxFileSize, Ignore: kbIgnoreGlobs()}
	scope := KBScopeRef{Kind: "project", ID: "p-test"}

	manifest1, err := cache.cachedScan(kbRoot, policy, "node-a", scope)
	require.NoError(t, err)
	digest1 := manifest1.ManifestDigest

	w, err := newKBWatcher(cache, 100*time.Millisecond)
	require.NoError(t, err)
	t.Cleanup(func() { _ = w.fsw.Close() })

	w.addTree(kbRoot)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go w.run(ctx)

	// Simulate the editor write-temp-rename pattern: write a temp file, then
	// rename it over the target. This produces Create + Rename events.
	tempPath := filepath.Join(kbRoot, ".doc.md.swp")
	require.NoError(t, os.WriteFile(tempPath, []byte("# Edited\n"), 0o644))
	require.NoError(t, os.Rename(tempPath, target))

	// Wait for the debounce window + fsnotify latency, then verify the edit
	// is visible in the manifest.
	deadline := time.After(2 * time.Second)
	for {
		manifest2, err := cache.cachedScan(kbRoot, policy, "node-a", scope)
		require.NoError(t, err)
		if manifest2.ManifestDigest != digest1 {
			// The edit is visible — the temp file should NOT be in the
			// manifest (it was renamed away), only the target.
			for _, e := range manifest2.Files {
				assert.NotEqual(t, ".doc.md.swp", e.Path, "temp file should not be in manifest")
			}
			return
		}
		select {
		case <-deadline:
			t.Fatal("watcher did not invalidate cache after write-temp-rename within 2s")
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// TestKBWatcher_FileDeletionInvalidatesCache verifies that deleting a file from
// the tree triggers cache invalidation so the manifest no longer lists it.
func TestKBWatcher_FileDeletionInvalidatesCache(t *testing.T) {
	tmp := t.TempDir()
	kbRoot := filepath.Join(tmp, ".horde", "knowledgebase")
	require.NoError(t, os.MkdirAll(kbRoot, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(kbRoot, "index.md"), []byte("# Index\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(kbRoot, "extra.md"), []byte("# Extra\n"), 0o644))

	cache := newKBManifestCache()
	policy := KBScopePolicy{MaxFileSize: DefaultKBMaxFileSize, Ignore: kbIgnoreGlobs()}
	scope := KBScopeRef{Kind: "project", ID: "p-test"}

	manifest1, err := cache.cachedScan(kbRoot, policy, "node-a", scope)
	require.NoError(t, err)
	assert.Len(t, manifest1.Files, 2)
	digest1 := manifest1.ManifestDigest

	w, err := newKBWatcher(cache, 50*time.Millisecond)
	require.NoError(t, err)
	t.Cleanup(func() { _ = w.fsw.Close() })

	w.addTree(kbRoot)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go w.run(ctx)

	// Delete a file.
	require.NoError(t, os.Remove(filepath.Join(kbRoot, "extra.md")))

	deadline := time.After(2 * time.Second)
	for {
		manifest2, err := cache.cachedScan(kbRoot, policy, "node-a", scope)
		require.NoError(t, err)
		if manifest2.ManifestDigest != digest1 {
			assert.Len(t, manifest2.Files, 1)
			assert.Equal(t, "index.md", manifest2.Files[0].Path)
			return
		}
		select {
		case <-deadline:
			t.Fatal("watcher did not invalidate cache after deletion within 2s")
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// TestKBWatcher_NewSubdirWatched verifies that creating a new subdirectory in
// the tree is picked up: the watcher adds the new dir so files created inside
// it trigger invalidation.
func TestKBWatcher_NewSubdirWatched(t *testing.T) {
	tmp := t.TempDir()
	kbRoot := filepath.Join(tmp, ".horde", "knowledgebase")
	require.NoError(t, os.MkdirAll(kbRoot, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(kbRoot, "index.md"), []byte("# Index\n"), 0o644))

	cache := newKBManifestCache()
	policy := KBScopePolicy{MaxFileSize: DefaultKBMaxFileSize, Ignore: kbIgnoreGlobs()}
	scope := KBScopeRef{Kind: "project", ID: "p-test"}

	manifest1, err := cache.cachedScan(kbRoot, policy, "node-a", scope)
	require.NoError(t, err)
	digest1 := manifest1.ManifestDigest

	w, err := newKBWatcher(cache, 50*time.Millisecond)
	require.NoError(t, err)
	t.Cleanup(func() { _ = w.fsw.Close() })

	w.addTree(kbRoot)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go w.run(ctx)

	// Create a new subdirectory, then a file inside it.
	conceptsDir := filepath.Join(kbRoot, "concepts")
	require.NoError(t, os.MkdirAll(conceptsDir, 0o755))
	// Give the watcher time to add the new dir before writing into it.
	time.Sleep(100 * time.Millisecond)
	require.NoError(t, os.WriteFile(filepath.Join(conceptsDir, "note.md"), []byte("# Note\n"), 0o644))

	deadline := time.After(3 * time.Second)
	for {
		manifest2, err := cache.cachedScan(kbRoot, policy, "node-a", scope)
		require.NoError(t, err)
		if manifest2.ManifestDigest != digest1 {
			// The new file in the subdirectory should be in the manifest.
			found := false
			for _, e := range manifest2.Files {
				if e.Path == "concepts/note.md" {
					found = true
				}
			}
			assert.True(t, found, "file in new subdirectory should be in manifest")
			return
		}
		select {
		case <-deadline:
			t.Fatal("watcher did not pick up file in new subdirectory within 3s")
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// TestKBWatcher_RemoveTreeStopsWatching verifies that removeTree stops watching
// a tree: edits to a removed tree do not trigger cache invalidation.
func TestKBWatcher_RemoveTreeStopsWatching(t *testing.T) {
	tmp := t.TempDir()
	kbRoot := filepath.Join(tmp, ".horde", "knowledgebase")
	require.NoError(t, os.MkdirAll(kbRoot, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(kbRoot, "index.md"), []byte("# Index\n"), 0o644))

	cache := newKBManifestCache()
	policy := KBScopePolicy{MaxFileSize: DefaultKBMaxFileSize, Ignore: kbIgnoreGlobs()}
	scope := KBScopeRef{Kind: "project", ID: "p-test"}

	manifest1, err := cache.cachedScan(kbRoot, policy, "node-a", scope)
	require.NoError(t, err)
	digest1 := manifest1.ManifestDigest

	w, err := newKBWatcher(cache, 50*time.Millisecond)
	require.NoError(t, err)
	t.Cleanup(func() { _ = w.fsw.Close() })

	w.addTree(kbRoot)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go w.run(ctx)

	// Remove the tree from the watcher.
	w.removeTree(kbRoot)

	// Edit a file — the watcher should NOT invalidate the cache.
	require.NoError(t, os.WriteFile(filepath.Join(kbRoot, "index.md"), []byte("# Edited\n"), 0o644))

	// Wait beyond the debounce + fsnotify latency.
	time.Sleep(300 * time.Millisecond)

	// The cache should still hold the old manifest (not invalidated).
	manifest2, err := cache.cachedScan(kbRoot, policy, "node-a", scope)
	require.NoError(t, err)
	assert.Equal(t, digest1, manifest2.ManifestDigest, "cache should not be invalidated after removeTree")
}

// TestKBWatcher_CtxCancelClosesWatcher verifies that canceling the context
// closes the fsnotify watcher and exits the run goroutine cleanly (no fd/goroutine
// leak — the -race goroutine-leak check depends on this).
func TestKBWatcher_CtxCancelClosesWatcher(t *testing.T) {
	tmp := t.TempDir()
	kbRoot := filepath.Join(tmp, ".horde", "knowledgebase")
	require.NoError(t, os.MkdirAll(kbRoot, 0o755))

	cache := newKBManifestCache()
	w, err := newKBWatcher(cache, 50*time.Millisecond)
	require.NoError(t, err)

	w.addTree(kbRoot)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		w.run(ctx)
		close(done)
	}()

	cancel()
	select {
	case <-done:
		// Goroutine exited cleanly.
	case <-time.After(2 * time.Second):
		t.Fatal("watcher goroutine did not exit within 2s of ctx cancel")
	}
}
