package server

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestScaffoldKnowledgebase_CreatesStructure(t *testing.T) {
	tmp := t.TempDir()

	err := scaffoldKnowledgebase(tmp, "test-project")
	require.NoError(t, err)

	kbDir := filepath.Join(tmp, ".horde", "knowledgebase")
	assert.DirExists(t, kbDir)
	assert.DirExists(t, filepath.Join(kbDir, "concepts"))
	assert.DirExists(t, filepath.Join(kbDir, "decisions"))
	assert.DirExists(t, filepath.Join(kbDir, "patterns"))
	assert.DirExists(t, filepath.Join(kbDir, "plans"))
	assert.DirExists(t, filepath.Join(kbDir, "references"))
}

func TestScaffoldKnowledgebase_SeedFiles(t *testing.T) {
	tmp := t.TempDir()

	err := scaffoldKnowledgebase(tmp, "my-proj")
	require.NoError(t, err)

	kbDir := filepath.Join(tmp, ".horde", "knowledgebase")

	// index.md should exist and contain the project name.
	indexPath := filepath.Join(kbDir, "index.md")
	assert.FileExists(t, indexPath)
	data, err := os.ReadFile(indexPath)
	require.NoError(t, err)
	assert.Contains(t, string(data), "my-proj knowledge base")
	assert.Contains(t, string(data), `okf_version: "0.1"`)

	// log.md should exist.
	logPath := filepath.Join(kbDir, "log.md")
	assert.FileExists(t, logPath)
	data, err = os.ReadFile(logPath)
	require.NoError(t, err)
	assert.Contains(t, string(data), "Initialization")

	// Category index files.
	for _, cat := range []string{"concepts", "decisions", "patterns", "plans", "references"} {
		catPath := filepath.Join(kbDir, cat, "index.md")
		assert.FileExists(t, catPath)
	}
}

func TestScaffoldKnowledgebase_DoesNotOverwrite(t *testing.T) {
	tmp := t.TempDir()

	// First scaffold creates the structure.
	require.NoError(t, scaffoldKnowledgebase(tmp, "first"))

	// Modify index.md to detect overwrite.
	indexPath := filepath.Join(tmp, ".horde", "knowledgebase", "index.md")
	require.NoError(t, os.WriteFile(indexPath, []byte("custom content"), 0o644))

	// Second scaffold should not overwrite.
	require.NoError(t, scaffoldKnowledgebase(tmp, "second"))

	data, err := os.ReadFile(indexPath)
	require.NoError(t, err)
	assert.Equal(t, "custom content", string(data))
}

func TestScaffoldKnowledgebase_NestedWorkspace(t *testing.T) {
	tmp := t.TempDir()
	workspace := filepath.Join(tmp, "deep", "nested", "path")

	err := scaffoldKnowledgebase(workspace, "nested-proj")
	require.NoError(t, err)
	assert.DirExists(t, filepath.Join(workspace, ".horde", "knowledgebase"))
}
