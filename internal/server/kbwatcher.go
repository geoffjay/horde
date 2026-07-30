package server

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/sirupsen/logrus"
)

// kbWatcher watches KB trees for filesystem changes, debounces rapid events
// (editor write-temp-rename, bulk writes), and invokes a callback when a
// debounced window expires. On the authority, the callback invalidates the
// manifest cache so the read API serves fresh data. On a participant with
// WatchLocal enabled (stage 2), the callback triggers an early convergence
// pass so the local edit is pushed (KSP §11).
//
// Lifecycle is ctx-driven (no Stop()): the goroutine started by run exits when
// ctx is canceled, closing the underlying fsnotify watcher and freeing its fds.
type kbWatcher struct {
	fsw      *fsnotify.Watcher
	onChange func(root string)
	debounce time.Duration

	// mu guards watched. The fsnotify watcher's own add/remove are goroutine-safe
	// but the watched map tracks which roots are under watch so removeTree is
	// idempotent and addTree avoids double-adding.
	mu      sync.Mutex
	watched map[string]bool // root → watched
}

// newKBWatcher creates a watcher that invokes onChange when a debounced tree
// change is detected. The debounce duration coalesces rapid events from the
// same tree (e.g. an editor that writes a temp file then renames it over the
// target produces two events within milliseconds).
func newKBWatcher(onChange func(root string), debounce time.Duration) (*kbWatcher, error) {
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	if debounce <= 0 {
		debounce = defaultKBDebounce
	}
	return &kbWatcher{
		fsw:      fsw,
		onChange: onChange,
		debounce: debounce,
		watched:  make(map[string]bool),
	}, nil
}

// defaultKBDebounce is the default debounce window when the config does not
// set one. 500ms is long enough to coalesce an editor's write-temp-rename cycle
// (typically <50ms) but short enough that a user sees the update promptly.
const defaultKBDebounce = 500 * time.Millisecond

// addTree adds the canonical KB tree for a project to the watcher. The tree
// root is watched recursively (fsnotify watches the directory; file events
// within it are delivered). Safe to call after run has started: fsnotify's
// Add is goroutine-safe.
func (w *kbWatcher) addTree(root string) {
	w.mu.Lock()
	if w.watched[root] {
		w.mu.Unlock()
		return
	}
	w.mu.Unlock()

	// Watch the root directory. fsnotify on Linux delivers events for files
	// within a watched directory; on macOS (kqueue) each file needs its own
	// watch, but the KB tree is small (OKF docs are kilobytes, tens of files),
	// so we watch the root and rely on the recursive walk below for subdirs.
	if err := w.fsw.Add(root); err != nil {
		logrus.WithError(err).WithField("kb_root", root).Warn("kb watcher: add root failed")
		return
	}
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		// Watch subdirectories so events in nested dirs (e.g.
		// concepts/, decisions/) are delivered on every platform.
		if err != nil {
			return nil //nolint:nilerr // walk continues past the errored entry
		}
		if !d.IsDir() || path == root {
			return nil
		}
		if err := w.fsw.Add(path); err != nil {
			logrus.WithError(err).WithField("kb_dir", path).Warn("kb watcher: add subdir failed")
		}
		return nil
	})

	// Lock only to update the watched map, not across WalkDir.
	w.mu.Lock()
	w.watched[root] = true
	w.mu.Unlock()
}

// removeTree stops watching a tree root. Called when a project is finished or
// deleted. Idempotent.
func (w *kbWatcher) removeTree(root string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.watched[root] {
		return
	}
	_ = w.fsw.Remove(root)
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil //nolint:nilerr // walk continues past the errored entry
		}
		if !d.IsDir() || path == root {
			return nil
		}
		_ = w.fsw.Remove(path)
		return nil
	})
	delete(w.watched, root)
}

// run is the event loop. It reads fsnotify events, debounces them per tree
// root, and invalidates the manifest cache when a debounced window expires.
// Exits when ctx is canceled, closing the fsnotify watcher.
func (w *kbWatcher) run(ctx context.Context) {
	// pending tracks per-root debounce timers. When a timer fires, the root's
	// cache entry is invalidated. A new event for the same root resets the
	// timer, coalescing rapid events.
	pending := make(map[string]*time.Timer)
	var pendingMu sync.Mutex

	// invalidateRoot fires after the debounce window with no new events for
	// a root. It invalidates the cache and cleans up the pending entry.
	invalidateRoot := func(root string) {
		pendingMu.Lock()
		delete(pending, root)
		pendingMu.Unlock()
		if w.onChange != nil {
			w.onChange(root)
		}
		logrus.WithField("kb_root", root).Debug("kb watcher: tree change processed")
	}

	// scheduleDebounce (re)starts the debounce timer for a root.
	scheduleDebounce := func(root string) {
		pendingMu.Lock()
		defer pendingMu.Unlock()
		if t, ok := pending[root]; ok {
			t.Stop()
		}
		pending[root] = time.AfterFunc(w.debounce, func() {
			invalidateRoot(root)
		})
	}

	for {
		select {
		case <-ctx.Done():
			// Cancel all pending timers before closing the watcher.
			pendingMu.Lock()
			for _, t := range pending {
				t.Stop()
			}
			pending = nil
			pendingMu.Unlock()
			_ = w.fsw.Close()
			return

		case event, ok := <-w.fsw.Events:
			if !ok {
				return // watcher closed
			}
			// Only invalidate on write/create/remove/rename events. chmod
			// doesn't change content.
			if !event.Has(fsnotify.Write | fsnotify.Create | fsnotify.Remove | fsnotify.Rename) {
				continue
			}
			// Resolve the tree root from the event path. The event path is
			// a file/dir within a watched tree; find which watched root it
			// belongs to.
			root := w.rootForPath(event.Name)
			if root == "" {
				continue
			}
			// If a new subdirectory was created, add it to the watcher so
			// events inside it are delivered.
			if event.Has(fsnotify.Create) {
				if isDir(event.Name) {
					_ = w.fsw.Add(event.Name)
				}
			}
			scheduleDebounce(root)

		case err, ok := <-w.fsw.Errors:
			if !ok {
				return
			}
			if err != nil {
				logrus.WithError(err).Warn("kb watcher: fsnotify error")
			}
		}
	}
}

// rootForPath resolves which watched tree root a given path belongs to. Returns
// "" if the path is not under any watched root.
func (w *kbWatcher) rootForPath(path string) string {
	w.mu.Lock()
	defer w.mu.Unlock()
	for root := range w.watched {
		if path == root || isPathUnder(path, root) {
			return root
		}
	}
	return ""
}

// isPathUnder reports whether path is inside dir (path starts with dir + sep).
func isPathUnder(path, dir string) bool {
	rel, err := filepath.Rel(dir, path)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	// filepath.Rel returns ".."-prefixed paths for paths outside dir.
	for _, seg := range splitPath(rel) {
		if seg == ".." {
			return false
		}
	}
	return true
}

// splitPath splits a slash-or-platform-sep path into segments.
func splitPath(p string) []string {
	p = filepath.ToSlash(p)
	var segs []string
	start := 0
	for i := range len(p) {
		if p[i] == '/' {
			if i > start {
				segs = append(segs, p[start:i])
			}
			start = i + 1
		}
	}
	if start < len(p) {
		segs = append(segs, p[start:])
	}
	return segs
}

// isDir reports whether path is a directory.
func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// kbEvent is a synthetic filesystem event for testing the debounce coalescing
// logic without a real fsnotify watcher. Timestamp is the event arrival time;
// Root is the tree root it belongs to.
//
//nolint:unused // used by kbwatcher_test.go (lint run.tests=false)
type kbEvent struct {
	TS   time.Time
	Root string
}

// coalesceEvents is the pure debounce core: given a sorted list of events
// (ordered by TS) and a debounce window, it returns the timestamps at which
// each root's coalesced window expires — i.e. the times the cache should be
// invalidated. Events within `debounce` of the last event for the same root
// are coalesced into one invalidation.
//
// This is extracted from run() so the debounce logic is unit-testable without
// a real fsnotify watcher or timers. The run() loop applies the same logic
// via time.AfterFunc.
//
//nolint:unused // used by kbwatcher_test.go (lint run.tests=false)
func coalesceEvents(events []kbEvent, debounce time.Duration) map[string]time.Time {
	// lastEvent tracks the latest event time per root.
	lastEvent := make(map[string]time.Time)
	for _, ev := range events {
		if prev, ok := lastEvent[ev.Root]; !ok || ev.TS.After(prev) {
			lastEvent[ev.Root] = ev.TS
		}
	}
	// Each root fires once, debounce after its last event.
	fires := make(map[string]time.Time)
	for root, ts := range lastEvent {
		fires[root] = ts.Add(debounce)
	}
	return fires
}
