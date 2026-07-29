package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/geoffjay/horde/internal/server"
)

// newKBTestServer creates a server with KB sync enabled and a project whose
// workspace has a scaffolded knowledgebase. Returns the server, router, and
// the project id.
func newKBTestServer(t *testing.T) (*server.Server, http.Handler, string) {
	t.Helper()

	workspace := t.TempDir()

	srv, err := server.New(server.Config{
		SpawnDefaultAgent: false,
		KBSync: server.KBSyncConfig{
			Enabled:     true,
			MaxFileSize: 1048576,
			Ignore:      []string{"*.tmp"},
		},
	})
	require.NoError(t, err)
	require.NoError(t, srv.Start(context.Background()))
	t.Cleanup(func() {})

	// Create a project with a scaffolded KB but no running agents.
	p, err := srv.CreateProjectForTest(server.CreateProjectInput{
		Name:       "test-proj",
		Workspace:  workspace,
		AgentNames: []string{"greeter"},
	})
	require.NoError(t, err)
	h := Router(srv)
	return srv, h, p.ID
}

func TestGetKBManifest_Disabled(t *testing.T) {
	srv := newTestServer(t)
	h := Router(srv)

	// Sync is disabled by default.
	w := do(t, h, http.MethodGet, "/api/v1/kb/project/p-1/manifest", nil)
	require.Equal(t, http.StatusNotImplemented, w.Code)
	assert.Equal(t, "false", w.Header().Get("X-KSP-Enabled"))
}

func TestGetKBManifest_UnregisteredKind(t *testing.T) {
	srv, err := server.New(server.Config{
		SpawnDefaultAgent: false,
		KBSync:            server.KBSyncConfig{Enabled: true},
	})
	require.NoError(t, err)
	require.NoError(t, srv.Start(context.Background()))
	t.Cleanup(func() {})
	h := Router(srv)

	w := do(t, h, http.MethodGet, "/api/v1/kb/team/t-1/manifest", nil)
	require.Equal(t, http.StatusNotFound, w.Code)
}

func TestGetKBManifest_UnknownProject(t *testing.T) {
	_, h, _ := newKBTestServer(t)

	w := do(t, h, http.MethodGet, "/api/v1/kb/project/nonexistent/manifest", nil)
	require.Equal(t, http.StatusNotFound, w.Code)
}

func TestGetKBManifest_Success(t *testing.T) {
	_, h, projectID := newKBTestServer(t)

	w := do(t, h, http.MethodGet, "/api/v1/kb/project/"+projectID+"/manifest", nil)
	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "authority", w.Header().Get("X-KSP-Authority"))

	var manifest server.KBManifest
	require.NoError(t, json.NewDecoder(w.Body).Decode(&manifest))
	assert.Equal(t, "project", manifest.Scope.Kind)
	assert.Equal(t, projectID, manifest.Scope.ID)
	assert.NotEmpty(t, manifest.ManifestDigest)
	// Scaffolded KB has index.md, log.md, and category index files.
	assert.Greater(t, len(manifest.Files), 0)
}

func TestGetKBManifest_IfNoneMatch_304(t *testing.T) {
	_, h, projectID := newKBTestServer(t)

	// First request to get the manifest digest.
	w := do(t, h, http.MethodGet, "/api/v1/kb/project/"+projectID+"/manifest", nil)
	require.Equal(t, http.StatusOK, w.Code)
	var manifest server.KBManifest
	require.NoError(t, json.NewDecoder(w.Body).Decode(&manifest))

	etag := "\"" + manifest.ManifestDigest + "\""
	assert.Equal(t, etag, w.Header().Get("ETag"))

	// Second request with If-None-Match → 304.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/kb/project/"+projectID+"/manifest", nil)
	req.Header.Set("If-None-Match", etag)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	require.Equal(t, http.StatusNotModified, rec.Code)
	assert.Equal(t, etag, rec.Header().Get("ETag"))
	assert.Equal(t, "authority", rec.Header().Get("X-KSP-Authority"))
}

func TestGetKBFile_Disabled(t *testing.T) {
	srv := newTestServer(t)
	h := Router(srv)

	w := do(t, h, http.MethodGet, "/api/v1/kb/project/p-1/file?path=index.md", nil)
	require.Equal(t, http.StatusNotImplemented, w.Code)
	assert.Equal(t, "false", w.Header().Get("X-KSP-Enabled"))
}

func TestGetKBFile_Success(t *testing.T) {
	_, h, projectID := newKBTestServer(t)

	// Get manifest to find a file path + digest.
	w := do(t, h, http.MethodGet, "/api/v1/kb/project/"+projectID+"/manifest", nil)
	require.Equal(t, http.StatusOK, w.Code)
	var manifest server.KBManifest
	require.NoError(t, json.NewDecoder(w.Body).Decode(&manifest))
	require.Greater(t, len(manifest.Files), 0)

	entry := manifest.Files[0]
	filePath := entry.Path

	// Fetch the file.
	w2 := do(t, h, http.MethodGet, "/api/v1/kb/project/"+projectID+"/file?path="+filePath, nil)
	require.Equal(t, http.StatusOK, w2.Code)
	assert.Equal(t, "authority", w2.Header().Get("X-KSP-Authority"))
	etag := "\"" + entry.Digest + "\""
	assert.Equal(t, etag, w2.Header().Get("ETag"))
	assert.NotEmpty(t, w2.Header().Get("Last-Modified"))
	assert.NotEmpty(t, w2.Body.Bytes())
}

func TestGetKBFile_NotFound(t *testing.T) {
	_, h, projectID := newKBTestServer(t)

	w := do(t, h, http.MethodGet, "/api/v1/kb/project/"+projectID+"/file?path=nonexistent.md", nil)
	require.Equal(t, http.StatusNotFound, w.Code)
}

func TestGetKBFile_MissingPathParam(t *testing.T) {
	_, h, projectID := newKBTestServer(t)

	w := do(t, h, http.MethodGet, "/api/v1/kb/project/"+projectID+"/file", nil)
	require.Equal(t, http.StatusBadRequest, w.Code)
}

func TestGetKBFile_InvalidPath(t *testing.T) {
	_, h, projectID := newKBTestServer(t)

	w := do(t, h, http.MethodGet, "/api/v1/kb/project/"+projectID+"/file?path=../../../etc/passwd", nil)
	require.Equal(t, http.StatusBadRequest, w.Code)
}

func TestGetKBFile_NewFileAppearsInManifest(t *testing.T) {
	srv, h, projectID := newKBTestServer(t)

	// Get manifest (cached).
	w := do(t, h, http.MethodGet, "/api/v1/kb/project/"+projectID+"/manifest", nil)
	require.Equal(t, http.StatusOK, w.Code)
	var manifest1 server.KBManifest
	require.NoError(t, json.NewDecoder(w.Body).Decode(&manifest1))
	digest1 := manifest1.ManifestDigest

	// Write a new file to the KB tree.
	project, err := srv.GetProject(projectID)
	require.NoError(t, err)
	kbPath := filepath.Join(project.Workspace, ".horde", "knowledgebase", "new-file.md")
	require.NoError(t, os.WriteFile(kbPath, []byte("# New\n"), 0o644))

	// Invalidate the cache so the next scan picks up the new file.
	srv.KBResolveScope("project").InvalidateCache(projectID)

	// Re-scan: new file should appear, digest should change.
	w2 := do(t, h, http.MethodGet, "/api/v1/kb/project/"+projectID+"/manifest", nil)
	require.Equal(t, http.StatusOK, w2.Code)
	var manifest2 server.KBManifest
	require.NoError(t, json.NewDecoder(w2.Body).Decode(&manifest2))
	assert.NotEqual(t, digest1, manifest2.ManifestDigest)
	assert.Greater(t, len(manifest2.Files), len(manifest1.Files))
}
