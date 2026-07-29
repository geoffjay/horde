package server

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestKBSyncRecordStore_InMemory(t *testing.T) {
	s := newKBSyncRecordStore("", "project", "p-1")

	// Missing record = "" (never synced, not clean).
	assert.Equal(t, "", s.Get("index.md"))

	// Set and get.
	s.Set("index.md", "sha256:abc")
	assert.Equal(t, "sha256:abc", s.Get("index.md"))

	s.Set("index.md", "sha256:def")
	assert.Equal(t, "sha256:def", s.Get("index.md"))

	// Delete.
	s.Delete("index.md")
	assert.Equal(t, "", s.Get("index.md"))

	// All returns a snapshot.
	s.Set("a.md", "sha256:a")
	s.Set("b.md", "sha256:b")
	all := s.All()
	assert.Equal(t, "sha256:a", all["a.md"])
	assert.Equal(t, "sha256:b", all["b.md"])
	assert.Len(t, all, 2)

	// Mutating the snapshot doesn't affect the store.
	all["c.md"] = "sha256:c"
	assert.Equal(t, "", s.Get("c.md"))
}

func TestKBSyncRecordStore_Persisted(t *testing.T) {
	stateDir := t.TempDir()

	s1 := newKBSyncRecordStore(stateDir, "project", "p-1")
	s1.Set("index.md", "sha256:abc")
	s1.Set("concepts/x.md", "sha256:def")

	// Verify the file was written.
	data, err := os.ReadFile(filepath.Join(stateDir, "kb-sync", "project", "p-1.json"))
	require.NoError(t, err)
	assert.Contains(t, string(data), "sha256:abc")

	// Create a new store for the same scope — it should load the records.
	s2 := newKBSyncRecordStore(stateDir, "project", "p-1")
	assert.Equal(t, "sha256:abc", s2.Get("index.md"))
	assert.Equal(t, "sha256:def", s2.Get("concepts/x.md"))
}

func TestKBSyncRecordStore_MissingFileIsFresh(t *testing.T) {
	stateDir := t.TempDir()
	s := newKBSyncRecordStore(stateDir, "project", "never-existed")
	assert.Equal(t, "", s.Get("index.md"))
	assert.Empty(t, s.All())
}

func TestKBSyncRecordStore_CorruptFileIsFresh(t *testing.T) {
	stateDir := t.TempDir()
	corruptPath := filepath.Join(stateDir, "kb-sync", "project", "p-1.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(corruptPath), 0o755))
	require.NoError(t, os.WriteFile(corruptPath, []byte("not json"), 0o644))

	s := newKBSyncRecordStore(stateDir, "project", "p-1")
	assert.Equal(t, "", s.Get("index.md"))
	assert.Empty(t, s.All())
}

func TestKBSyncStoreManager(t *testing.T) {
	stateDir := t.TempDir()
	m := newKBSyncStoreManager(stateDir)

	// Same scope returns the same store.
	s1 := m.Get("project", "p-1")
	s2 := m.Get("project", "p-1")
	assert.Same(t, s1, s2)

	// Different scope returns a different store.
	s3 := m.Get("project", "p-2")
	assert.NotSame(t, s1, s3)

	// Records don't leak between scopes.
	s1.Set("index.md", "sha256:a")
	assert.Equal(t, "", s3.Get("index.md"))
}
