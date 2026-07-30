package server

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestKBConflictArea_Disabled(t *testing.T) {
	ca := newKBConflictArea("")
	_, err := ca.Preserve(KBScopeRef{Kind: "project", ID: "p-1"}, "index.md", "/some/path")
	assert.Error(t, err)
}

func TestKBConflictArea_Preserve(t *testing.T) {
	dataDir := t.TempDir()
	ca := newKBConflictArea(dataDir)

	// Create a local file to preserve.
	localDir := t.TempDir()
	localPath := filepath.Join(localDir, "index.md")
	require.NoError(t, os.WriteFile(localPath, []byte("# My Edit\n"), 0o644))

	scope := KBScopeRef{Kind: "project", ID: "p-1"}
	conflictPath, err := ca.Preserve(scope, "index.md", localPath)
	require.NoError(t, err)

	// The conflict copy should exist and contain the original content.
	data, err := os.ReadFile(conflictPath)
	require.NoError(t, err)
	assert.Equal(t, "# My Edit\n", string(data))

	// The conflict copy should be under <dataDir>/kb-conflicts/<kind>/<id>/.
	assert.Contains(t, conflictPath, filepath.Join(dataDir, "kb-conflicts", "project", "p-1"))
}

func TestKBConflictArea_UniqueNamesForRepeatedConflicts(t *testing.T) {
	dataDir := t.TempDir()
	ca := newKBConflictArea(dataDir)

	localDir := t.TempDir()
	localPath := filepath.Join(localDir, "doc.md")
	scope := KBScopeRef{Kind: "project", ID: "p-1"}

	// Preserve the same path twice with different content — each copy must
	// be uniquely named so they don't clobber each other (KSP §6.1).
	require.NoError(t, os.WriteFile(localPath, []byte("v1"), 0o644))
	path1, err := ca.Preserve(scope, "doc.md", localPath)
	require.NoError(t, err)

	require.NoError(t, os.WriteFile(localPath, []byte("v2"), 0o644))
	path2, err := ca.Preserve(scope, "doc.md", localPath)
	require.NoError(t, err)

	assert.NotEqual(t, path1, path2, "conflict copies must have unique names")

	// Both copies should still exist with their own content.
	data1, err := os.ReadFile(path1)
	require.NoError(t, err)
	assert.Equal(t, "v1", string(data1))

	data2, err := os.ReadFile(path2)
	require.NoError(t, err)
	assert.Equal(t, "v2", string(data2))
}

func TestKBConflictArea_OutsideKBTree(t *testing.T) {
	dataDir := t.TempDir()
	ca := newKBConflictArea(dataDir)

	localDir := t.TempDir()
	localPath := filepath.Join(localDir, "file.md")
	require.NoError(t, os.WriteFile(localPath, []byte("content"), 0o644))

	scope := KBScopeRef{Kind: "project", ID: "p-1"}
	conflictPath, err := ca.Preserve(scope, "file.md", localPath)
	require.NoError(t, err)

	// The conflict area must be outside the KB tree (KSP §6.1).
	// It should be under <dataDir>/kb-conflicts/, not under the local tree.
	assert.Contains(t, conflictPath, "kb-conflicts")
	assert.NotContains(t, conflictPath, filepath.Join(localDir))
}

func TestEscapePath(t *testing.T) {
	assert.Equal(t, "concepts_x.md", escapePath("concepts/x.md"))
	assert.Equal(t, "a_b_c", escapePath("a/b/c"))
	assert.Equal(t, "flat.md", escapePath("flat.md"))
}

func TestKBConflictArea_List(t *testing.T) {
	dataDir := t.TempDir()
	ca := newKBConflictArea(dataDir)

	localDir := t.TempDir()
	scope := KBScopeRef{Kind: "project", ID: "p-1"}

	// Preserve two conflicts at different paths.
	require.NoError(t, os.WriteFile(filepath.Join(localDir, "a.md"), []byte("content-a"), 0o644))
	_, err := ca.Preserve(scope, "concepts/a.md", filepath.Join(localDir, "a.md"))
	require.NoError(t, err)

	require.NoError(t, os.WriteFile(filepath.Join(localDir, "b.md"), []byte("content-b"), 0o644))
	_, err = ca.Preserve(scope, "decisions/b.md", filepath.Join(localDir, "b.md"))
	require.NoError(t, err)

	// List should return both.
	entries, err := ca.List(scope)
	require.NoError(t, err)
	assert.Len(t, entries, 2)

	// Each entry should have the scope and a non-empty path.
	paths := make(map[string]bool)
	for _, e := range entries {
		assert.Equal(t, scope, e.Scope)
		assert.NotEmpty(t, e.Path)
		assert.NotEmpty(t, e.Digest)
		paths[e.Path] = true
	}
	assert.True(t, paths["concepts/a.md"], "should list concepts/a.md")
	assert.True(t, paths["decisions/b.md"], "should list decisions/b.md")

	// A different scope should have no entries.
	other := KBScopeRef{Kind: "project", ID: "p-2"}
	otherEntries, err := ca.List(other)
	require.NoError(t, err)
	assert.Empty(t, otherEntries)

	// Disabled conflict area returns nil.
	disabled := newKBConflictArea("")
	disabledEntries, err := disabled.List(scope)
	require.NoError(t, err)
	assert.Nil(t, disabledEntries)
}
