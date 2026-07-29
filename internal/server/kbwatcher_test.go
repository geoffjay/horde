package server

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCoalesceEvents_SingleEvent(t *testing.T) {
	ts := time.Unix(1000, 0)
	events := []kbEvent{{TS: ts, Root: "/kb/proj1"}}
	fires := coalesceEvents(events, 500*time.Millisecond)
	assert.Equal(t, ts.Add(500*time.Millisecond), fires["/kb/proj1"])
}

func TestCoalesceEvents_MultipleEventsSameRootCoalesced(t *testing.T) {
	// Three events for the same root within the debounce window should
	// coalesce to a single fire at last-event + debounce.
	t0 := time.Unix(1000, 0)
	events := []kbEvent{
		{TS: t0, Root: "/kb/proj1"},
		{TS: t0.Add(100 * time.Millisecond), Root: "/kb/proj1"},
		{TS: t0.Add(200 * time.Millisecond), Root: "/kb/proj1"},
	}
	fires := coalesceEvents(events, 500*time.Millisecond)
	assert.Len(t, fires, 1)
	assert.Equal(t, t0.Add(200*time.Millisecond).Add(500*time.Millisecond), fires["/kb/proj1"])
}

func TestCoalesceEvents_MultipleRootsIndependent(t *testing.T) {
	// Events for different roots are debounced independently.
	t0 := time.Unix(1000, 0)
	events := []kbEvent{
		{TS: t0, Root: "/kb/proj1"},
		{TS: t0.Add(100 * time.Millisecond), Root: "/kb/proj2"},
		{TS: t0.Add(200 * time.Millisecond), Root: "/kb/proj1"},
	}
	fires := coalesceEvents(events, 500*time.Millisecond)
	assert.Len(t, fires, 2)
	assert.Equal(t, t0.Add(200*time.Millisecond).Add(500*time.Millisecond), fires["/kb/proj1"])
	assert.Equal(t, t0.Add(100*time.Millisecond).Add(500*time.Millisecond), fires["/kb/proj2"])
}

func TestCoalesceEvents_Empty(t *testing.T) {
	fires := coalesceEvents(nil, 500*time.Millisecond)
	assert.Empty(t, fires)
}

func TestCoalesceEvents_OutOfOrderKeepsLatest(t *testing.T) {
	// Events arriving out of order: the latest TS wins.
	t0 := time.Unix(1000, 0)
	events := []kbEvent{
		{TS: t0.Add(200 * time.Millisecond), Root: "/kb/proj1"},
		{TS: t0, Root: "/kb/proj1"},
		{TS: t0.Add(100 * time.Millisecond), Root: "/kb/proj1"},
	}
	fires := coalesceEvents(events, 500*time.Millisecond)
	assert.Equal(t, t0.Add(200*time.Millisecond).Add(500*time.Millisecond), fires["/kb/proj1"])
}

func TestIsPathUnder(t *testing.T) {
	tests := []struct {
		name string
		path string
		dir  string
		want bool
	}{
		{"exact match", "/kb/proj1", "/kb/proj1", true},
		{"nested file", "/kb/proj1/index.md", "/kb/proj1", true},
		{"nested dir", "/kb/proj1/concepts/x.md", "/kb/proj1", true},
		{"sibling", "/kb/proj2/index.md", "/kb/proj1", false},
		{"parent", "/kb", "/kb/proj1", false},
		{"unrelated", "/other/path", "/kb/proj1", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, isPathUnder(tc.path, tc.dir))
		})
	}
}

func TestSplitPath(t *testing.T) {
	tests := []struct {
		name string
		path string
		want []string
	}{
		{"simple", "a/b/c", []string{"a", "b", "c"}},
		{"trailing slash", "a/b/", []string{"a", "b"}},
		{"leading slash", "/a/b", []string{"a", "b"}},
		{"single", "a", []string{"a"}},
		{"empty", "", nil},
		{"double slash", "a//b", []string{"a", "b"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, splitPath(tc.path))
		})
	}
}

func TestRootForPath(t *testing.T) {
	w := &kbWatcher{watched: map[string]bool{
		"/kb/proj1": true,
		"/kb/proj2": true,
	}}
	tests := []struct {
		name string
		path string
		want string
	}{
		{"exact root", "/kb/proj1", "/kb/proj1"},
		{"nested file", "/kb/proj1/index.md", "/kb/proj1"},
		{"nested deep", "/kb/proj1/concepts/x.md", "/kb/proj1"},
		{"not under any root", "/other/path", ""},
		{"sibling root", "/kb/proj2/concepts/y.md", "/kb/proj2"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, w.rootForPath(tc.path))
		})
	}
}

func TestKBWatcher_AddRemoveTree(t *testing.T) {
	// This is a unit test of the watched map tracking, not the real fsnotify
	// watcher (which needs real fds — integration tests cover that). We
	// create a watcher with a real fsnotify watcher but only test the map
	// state, using temp dirs that exist on disk.
	tmp := t.TempDir()
	kbRoot := filepath.Join(tmp, "kb")
	require.NoError(t, os.MkdirAll(kbRoot, 0o755))

	cache := newKBManifestCache()
	w, err := newKBWatcher(cache, 50*time.Millisecond)
	require.NoError(t, err)
	t.Cleanup(func() { _ = w.fsw.Close() })

	// addTree should track the root.
	w.addTree(kbRoot)
	assert.True(t, w.watched[kbRoot])

	// Adding again is a no-op (idempotent).
	w.addTree(kbRoot)
	assert.True(t, w.watched[kbRoot])

	// removeTree stops tracking.
	w.removeTree(kbRoot)
	assert.False(t, w.watched[kbRoot])

	// Removing again is a no-op (idempotent).
	w.removeTree(kbRoot)
}
