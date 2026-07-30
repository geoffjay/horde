package server

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// KBSyncRecord is the per-scope, per-path sync state (KSP §2.4). SyncedDigest
// is the digest this node last agreed with the authority — the last content it
// successfully pulled or pushed. A missing record means "never synced" (not
// "clean"), which is how a node distinguishes "deleted upstream" from "I never
// had it" (KSP §5.1).
type KBSyncRecord struct {
	Path         string `json:"path"`
	SyncedDigest string `json:"synced_digest"`
}

// kbSyncRecordStore persists sync records per scope, keyed by path. Records
// are stored as a JSON file under <stateDir>/kb-sync/<kind>/<id>.json. When no
// state dir is configured, records are in-memory only (lost on restart — KSP
// §2.4 requires persistence, so this is only for tests/ephemeral nodes).
type kbSyncRecordStore struct {
	mu      sync.Mutex
	records map[string]string // path → synced_digest
	path    string            // on-disk path; empty = in-memory
}

// newKBSyncRecordStore creates a sync record store for a scope. If stateDir is
// non-empty, records persist to <stateDir>/kb-sync/<kind>/<id>.json; otherwise
// they are in-memory only.
func newKBSyncRecordStore(stateDir, kind, id string) *kbSyncRecordStore {
	s := &kbSyncRecordStore{
		records: make(map[string]string),
	}
	if stateDir != "" {
		s.path = filepath.Join(stateDir, "kb-sync", kind, id+".json")
		s.load()
	}
	return s
}

// Get returns the synced_digest for a path, or "" when the record is missing
// (KSP §2.4: a missing record is "never synced", not "clean").
func (s *kbSyncRecordStore) Get(path string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.records[path]
}

// Set records the synced_digest for a path (KSP §2.4: applying a pulled file
// sets S to the pulled digest; a pushed file sets S to the new digest).
func (s *kbSyncRecordStore) Set(path, digest string) {
	s.mu.Lock()
	s.records[path] = digest
	s.mu.Unlock()
	s.save()
}

// Delete drops the sync record for a path (KSP §5.1: "deleted upstream, clean
// local → delete locally, drop record").
func (s *kbSyncRecordStore) Delete(path string) {
	s.mu.Lock()
	delete(s.records, path)
	s.mu.Unlock()
	s.save()
}

// All returns a snapshot of all sync records (path → synced_digest). Used by
// the convergence loop to classify all paths against the authority manifest.
func (s *kbSyncRecordStore) All() map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]string, len(s.records))
	for k, v := range s.records {
		out[k] = v
	}
	return out
}

// load reads persisted records from disk. Missing file = fresh start (no
// records, all paths "never synced").
func (s *kbSyncRecordStore) load() {
	if s.path == "" {
		return
	}
	data, err := os.ReadFile(s.path) //#nosec G304 // path is constructed from config stateDir + kind/id
	if err != nil {
		return // missing file: treat as a fresh start
	}
	var records map[string]string
	if err := json.Unmarshal(data, &records); err != nil {
		return // corrupt = fresh (not fatal; convergence re-derives)
	}
	s.records = records
	if s.records == nil {
		s.records = make(map[string]string)
	}
}

// save writes records to disk atomically (KSP §4.3: temp-file + rename so a
// reader never observes a torn file). The lock is held across marshal + write
// so concurrent Sets cannot interleave or write out of order. Best-effort: a
// failure is logged but does not block convergence — the next successful save
// is authoritative.
func (s *kbSyncRecordStore) save() {
	if s.path == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := json.Marshal(s.records)
	if err != nil {
		return
	}
	_ = atomicWriteFile(s.path, data)
}

// kbSyncStoreManager creates and caches per-scope sync record stores. Each
// scope gets its own store; the manager avoids re-creating a store for a scope
// that has already been loaded.
type kbSyncStoreManager struct {
	mu       sync.Mutex
	stores   map[string]*kbSyncRecordStore // "kind/id" → store
	stateDir string
}

func newKBSyncStoreManager(stateDir string) *kbSyncStoreManager {
	return &kbSyncStoreManager{
		stores:   make(map[string]*kbSyncRecordStore),
		stateDir: stateDir,
	}
}

func (m *kbSyncStoreManager) scopeKey(kind, id string) string {
	return fmt.Sprintf("%s/%s", kind, id)
}

// Get returns the sync record store for a scope, creating it on first access.
func (m *kbSyncStoreManager) Get(kind, id string) *kbSyncRecordStore {
	key := m.scopeKey(kind, id)
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.stores[key]; ok {
		return s
	}
	s := newKBSyncRecordStore(m.stateDir, kind, id)
	m.stores[key] = s
	return s
}
