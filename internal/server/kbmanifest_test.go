package server

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestKBValidatePath(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{"simple", "index.md", "index.md", false},
		{"nested", "concepts/arch.md", "concepts/arch.md", false},
		{"clean dot", "./index.md", "index.md", false},
		{"clean double dot in name", "a.b.md", "a.b.md", false},
		{"absolute", "/etc/passwd", "", true},
		{"traversal", "../secret", "", true},
		{"traversal nested", "concepts/../../secret", "", true},
		{"backslash", "concepts\\arch.md", "", true},
		{"empty", "", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ValidateKBPath(tt.input)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestKBMatchesIgnore(t *testing.T) {
	globs := []string{"*.tmp", "*.swp", ".git/**"}
	tests := []struct {
		path string
		want bool
	}{
		{"index.md", false},
		{"notes.tmp", true},
		{"file.swp", true},
		{".git/config", true},
		{".git/objects/ab/cdef", true},
		{"concepts/index.md", false},
		{"concepts/draft.tmp", false}, // *.tmp only matches root-level
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			assert.Equal(t, tt.want, kbMatchesIgnore(tt.path, globs))
		})
	}
}

func TestKBMatchGlob(t *testing.T) {
	tests := []struct {
		path    string
		pattern string
		want    bool
	}{
		{"index.md", "*.md", true},
		{"concepts/index.md", "*.md", false},
		{"concepts/index.md", "**/*.md", true},
		{".git/objects/ab/cdef", ".git/**", true},
		{".git/config", ".git/**", true},
		{"other/file", ".git/**", false},
		{"a/b/c/file.md", "**/*.md", true},
	}
	for _, tt := range tests {
		t.Run(tt.path+"~"+tt.pattern, func(t *testing.T) {
			assert.Equal(t, tt.want, kbMatchGlob(tt.path, tt.pattern))
		})
	}
}

func TestScanManifest(t *testing.T) {
	tmp := t.TempDir()
	kbRoot := filepath.Join(tmp, ".horde", "knowledgebase")
	require.NoError(t, os.MkdirAll(kbRoot, 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(kbRoot, "concepts"), 0o755))

	require.NoError(t, os.WriteFile(filepath.Join(kbRoot, "index.md"), []byte("# Test\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(kbRoot, "concepts", "arch.md"), []byte("# Architecture\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(kbRoot, "draft.tmp"), []byte("temp"), 0o644))

	policy := KBScopePolicy{
		MaxFileSize: 1048576,
		Ignore:      []string{"*.tmp"},
	}
	scope := KBScopeRef{Kind: "project", ID: "p-1"}

	manifest, err := scanManifest(kbRoot, policy, "node-a", scope)
	require.NoError(t, err)

	assert.Equal(t, "node-a", manifest.Authority)
	assert.Equal(t, scope, manifest.Scope)

	// Two files (index.md and concepts/arch.md); draft.tmp is ignored.
	assert.Len(t, manifest.Files, 2)

	// Entries are sorted by path.
	assert.Equal(t, "concepts/arch.md", manifest.Files[0].Path)
	assert.Equal(t, "index.md", manifest.Files[1].Path)

	// Each entry has a sha256: digest.
	for _, e := range manifest.Files {
		assert.True(t, len(e.Digest) > len(kbDigestPrefix))
		assert.Equal(t, kbDigestPrefix, e.Digest[:len(kbDigestPrefix)])
	}

	// Manifest digest is non-empty and deterministic.
	assert.NotEmpty(t, manifest.ManifestDigest)

	// Re-scan produces the same digest.
	manifest2, err := scanManifest(kbRoot, policy, "node-a", scope)
	require.NoError(t, err)
	assert.Equal(t, manifest.ManifestDigest, manifest2.ManifestDigest)
}

func TestScanManifest_EmptyTree(t *testing.T) {
	tmp := t.TempDir()
	kbRoot := filepath.Join(tmp, "kb")
	require.NoError(t, os.MkdirAll(kbRoot, 0o755))

	policy := KBScopePolicy{MaxFileSize: 1048576, Ignore: []string{}}
	scope := KBScopeRef{Kind: "project", ID: "p-empty"}

	manifest, err := scanManifest(kbRoot, policy, "node-a", scope)
	require.NoError(t, err)
	assert.Empty(t, manifest.Files)
	assert.NotEmpty(t, manifest.ManifestDigest) // digest of empty set
}

func TestScanManifest_SizeCap(t *testing.T) {
	tmp := t.TempDir()
	kbRoot := filepath.Join(tmp, "kb")
	require.NoError(t, os.MkdirAll(kbRoot, 0o755))

	require.NoError(t, os.WriteFile(filepath.Join(kbRoot, "small.md"), []byte("x"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(kbRoot, "big.md"), []byte("xxxxxxxxxxxx"), 0o644))

	policy := KBScopePolicy{MaxFileSize: 5, Ignore: []string{}}
	manifest, err := scanManifest(kbRoot, policy, "node-a", KBScopeRef{Kind: "project", ID: "p-1"})
	require.NoError(t, err)

	// Only small.md (1 byte) fits under the 5-byte cap.
	assert.Len(t, manifest.Files, 1)
	assert.Equal(t, "small.md", manifest.Files[0].Path)
}

func TestKBManifestCache_CacheHit(t *testing.T) {
	tmp := t.TempDir()
	kbRoot := filepath.Join(tmp, "kb")
	require.NoError(t, os.MkdirAll(kbRoot, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(kbRoot, "index.md"), []byte("hello"), 0o644))

	cache := newKBManifestCache()
	policy := KBScopePolicy{MaxFileSize: 1048576, Ignore: []string{}}
	scope := KBScopeRef{Kind: "project", ID: "p-1"}

	m1, err := cache.cachedScan(kbRoot, policy, "node-a", scope)
	require.NoError(t, err)

	m2, err := cache.cachedScan(kbRoot, policy, "node-a", scope)
	require.NoError(t, err)

	// Same object from cache (no re-scan).
	assert.Same(t, m1, m2)
}

func TestKBManifestCache_Invalidate(t *testing.T) {
	tmp := t.TempDir()
	kbRoot := filepath.Join(tmp, "kb")
	require.NoError(t, os.MkdirAll(kbRoot, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(kbRoot, "index.md"), []byte("hello"), 0o644))

	cache := newKBManifestCache()
	policy := KBScopePolicy{MaxFileSize: 1048576, Ignore: []string{}}
	scope := KBScopeRef{Kind: "project", ID: "p-1"}

	m1, err := cache.cachedScan(kbRoot, policy, "node-a", scope)
	require.NoError(t, err)

	cache.invalidate(kbRoot)

	// Re-scan after invalidation returns a new object.
	m2, err := cache.cachedScan(kbRoot, policy, "node-a", scope)
	require.NoError(t, err)
	assert.NotSame(t, m1, m2)
}

func TestKBReadFile(t *testing.T) {
	tmp := t.TempDir()
	kbRoot := filepath.Join(tmp, "kb")
	require.NoError(t, os.MkdirAll(kbRoot, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(kbRoot, "index.md"), []byte("# Hello\n"), 0o644))

	data, digest, modTime, err := kbReadFile(kbRoot, "index.md")
	require.NoError(t, err)
	assert.Equal(t, "# Hello\n", string(data))
	assert.True(t, len(digest) > len(kbDigestPrefix))
	assert.False(t, modTime.IsZero())
}

func TestKBReadFile_NotFound(t *testing.T) {
	tmp := t.TempDir()
	kbRoot := filepath.Join(tmp, "kb")
	require.NoError(t, os.MkdirAll(kbRoot, 0o755))

	_, _, _, err := kbReadFile(kbRoot, "nonexistent.md")
	assert.Error(t, err)
}

func TestKBReadFile_InvalidPath(t *testing.T) {
	tmp := t.TempDir()
	kbRoot := filepath.Join(tmp, "kb")
	require.NoError(t, os.MkdirAll(kbRoot, 0o755))

	_, _, _, err := kbReadFile(kbRoot, "../secret")
	assert.Error(t, err)
}

func TestKBFindEntry(t *testing.T) {
	manifest := &KBManifest{
		Files: []KBEntry{
			{Path: "concepts/arch.md", Digest: "sha256:aaa"},
			{Path: "index.md", Digest: "sha256:bbb"},
			{Path: "plans/roadmap.md", Digest: "sha256:ccc"},
		},
	}

	tests := []struct {
		path string
		want string
	}{
		{"index.md", "sha256:bbb"},
		{"concepts/arch.md", "sha256:aaa"},
		{"plans/roadmap.md", "sha256:ccc"},
		{"missing.md", ""},
		{"../escape", ""},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			e := KBFindEntry(manifest, tt.path)
			if tt.want == "" {
				assert.Nil(t, e)
			} else {
				require.NotNil(t, e)
				assert.Equal(t, tt.want, e.Digest)
			}
		})
	}
}

func TestKBResolveScope_UnregisteredKind(t *testing.T) {
	srv := &Server{}
	// No kbScopes registered (sync disabled).
	assert.Nil(t, srv.KBResolveScope("project"))
	assert.Nil(t, srv.KBResolveScope("team"))
	assert.Nil(t, srv.KBResolveScope("user"))
	assert.Nil(t, srv.KBResolveScope("cluster"))
	assert.Nil(t, srv.KBResolveScope("nonexistent"))
}

func TestKBSyncEnabled_DisabledByDefault(t *testing.T) {
	srv := &Server{cfg: Config{}}
	assert.False(t, srv.KBSyncEnabled())
}

func TestKBSyncEnabled_Enabled(t *testing.T) {
	srv := &Server{cfg: Config{KBSync: KBSyncConfig{Enabled: true}}}
	assert.True(t, srv.KBSyncEnabled())
}

func TestComputeManifestDigest_Deterministic(t *testing.T) {
	entries := []KBEntry{
		{Path: "index.md", Digest: "sha256:abc"},
		{Path: "concepts/arch.md", Digest: "sha256:def"},
	}
	d1 := computeManifestDigest(entries)

	// Same entries, different order → same digest (digest is over sorted pairs).
	reversed := []KBEntry{
		{Path: "concepts/arch.md", Digest: "sha256:def"},
		{Path: "index.md", Digest: "sha256:abc"},
	}
	d2 := computeManifestDigest(reversed)
	// Note: computeManifestDigest doesn't re-sort; it hashes in the given order.
	// The caller (scanManifest) sorts before calling. So different order → different digest.
	// This test confirms the function is deterministic for the same input.
	d3 := computeManifestDigest(entries)
	assert.Equal(t, d1, d3)
	assert.NotEqual(t, d1, d2) // different order → different digest
}
