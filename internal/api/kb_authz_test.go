package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/geoffjay/horde/internal/server"
)

// newKBAuthTestServer creates a server with per-user auth enabled (Users
// configured) and KB sync enabled. The server has two users:
//   - alice (team member, non-admin, token "alice-token")
//   - bob   (non-team, non-admin, token "bob-token")
//   - admin (admin, token "admin-token")
//
// The cluster auth token is "cluster-secret" so node-principal requests
// can be tested. The project owner is alice.
//
// Returns the server, router, project id, and alice's token.
func newKBAuthTestServer(t *testing.T) (*server.Server, http.Handler, string) {
	t.Helper()

	workspace := t.TempDir()

	srv, err := server.New(server.Config{
		SpawnDefaultAgent: false,
		AuthToken:         "cluster-secret",
		Users: []server.UserAuth{
			{ID: "alice", Token: "alice-token", Admin: false},
			{ID: "bob", Token: "bob-token", Admin: false},
			{ID: "admin", Token: "admin-token", Admin: true},
		},
		KBSync: server.KBSyncConfig{
			Enabled:     true,
			MaxFileSize: 1048576,
			Ignore:      []string{"*.tmp"},
		},
	})
	require.NoError(t, err)
	require.NoError(t, srv.Start(context.Background()))

	p, err := srv.CreateProjectForTest(server.CreateProjectInput{
		Name:       "auth-proj",
		Workspace:  workspace,
		AgentNames: []string{"greeter"},
		Owner:      "alice",
	})
	require.NoError(t, err)

	// Add alice as a team member (she's also the owner).
	_, err = srv.AddUserToProject(p.ID, "alice")
	require.NoError(t, err)

	h := Router(srv)
	return srv, h, p.ID
}

// doWithToken is like do but sets an Authorization: Bearer header.
func doWithToken(t *testing.T, h http.Handler, method, target, token string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body != nil {
		b, err := json.Marshal(body)
		require.NoError(t, err)
		r = httptest.NewRequest(method, target, bytes.NewReader(b))
		r.Header.Set("Content-Type", "application/json")
	} else {
		r = httptest.NewRequest(method, target, nil)
	}
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// --- Manifest read authz ---

func TestKBAuthz_Manifest_TeamMember_200(t *testing.T) {
	_, h, pid := newKBAuthTestServer(t)
	w := doWithToken(t, h, http.MethodGet, "/api/v1/kb/project/"+pid+"/manifest", "alice-token", nil)
	require.Equal(t, http.StatusOK, w.Code)
}

func TestKBAuthz_Manifest_Admin_200(t *testing.T) {
	_, h, pid := newKBAuthTestServer(t)
	w := doWithToken(t, h, http.MethodGet, "/api/v1/kb/project/"+pid+"/manifest", "admin-token", nil)
	require.Equal(t, http.StatusOK, w.Code)
}

func TestKBAuthz_Manifest_NonTeam_403(t *testing.T) {
	_, h, pid := newKBAuthTestServer(t)
	w := doWithToken(t, h, http.MethodGet, "/api/v1/kb/project/"+pid+"/manifest", "bob-token", nil)
	require.Equal(t, http.StatusForbidden, w.Code)
}

func TestKBAuthz_Manifest_Anonymous_403(t *testing.T) {
	_, h, pid := newKBAuthTestServer(t)
	w := doWithToken(t, h, http.MethodGet, "/api/v1/kb/project/"+pid+"/manifest", "", nil)
	require.Equal(t, http.StatusForbidden, w.Code)
}

func TestKBAuthz_Manifest_NodePrincipal_200(t *testing.T) {
	// A node principal (cluster token) may read the manifest for
	// convergence (KSP §9).
	_, h, pid := newKBAuthTestServer(t)
	w := doWithToken(t, h, http.MethodGet, "/api/v1/kb/project/"+pid+"/manifest", "cluster-secret", nil)
	require.Equal(t, http.StatusOK, w.Code)
}

// --- File read authz ---

func TestKBAuthz_File_TeamMember_200(t *testing.T) {
	_, h, pid := newKBAuthTestServer(t)
	// First get the manifest to find a valid path.
	wm := doWithToken(t, h, http.MethodGet, "/api/v1/kb/project/"+pid+"/manifest", "alice-token", nil)
	require.Equal(t, http.StatusOK, wm.Code)
	var manifest server.KBManifest
	require.NoError(t, json.NewDecoder(wm.Body).Decode(&manifest))
	require.Greater(t, len(manifest.Files), 0)
	path := manifest.Files[0].Path

	wf := doWithToken(t, h, http.MethodGet, "/api/v1/kb/project/"+pid+"/file?path="+path, "alice-token", nil)
	require.Equal(t, http.StatusOK, wf.Code)
}

func TestKBAuthz_File_NonTeam_403(t *testing.T) {
	_, h, pid := newKBAuthTestServer(t)
	w := doWithToken(t, h, http.MethodGet, "/api/v1/kb/project/"+pid+"/file?path=concepts/index.md", "bob-token", nil)
	require.Equal(t, http.StatusForbidden, w.Code)
}

func TestKBAuthz_File_NodePrincipal_200(t *testing.T) {
	_, h, pid := newKBAuthTestServer(t)
	w := doWithToken(t, h, http.MethodGet, "/api/v1/kb/project/"+pid+"/file?path=concepts/index.md", "cluster-secret", nil)
	require.Equal(t, http.StatusOK, w.Code)
}

// --- File write authz ---

func TestKBAuthz_Put_Owner_200(t *testing.T) {
	_, h, pid := newKBAuthTestServer(t)
	body := map[string]string{"path": "concepts/test.md", "content": "# test"}
	b, err := json.Marshal(body)
	require.NoError(t, err)

	r := httptest.NewRequest(http.MethodPut, "/api/v1/kb/project/"+pid+"/file?path=concepts/test.md", bytes.NewReader(b))
	r.Header.Set("Content-Type", "text/plain")
	r.Header.Set("If-None-Match", "*")
	r.Header.Set("Authorization", "Bearer alice-token")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestKBAuthz_Put_NonTeam_403(t *testing.T) {
	_, h, pid := newKBAuthTestServer(t)
	body := map[string]string{"path": "concepts/test.md", "content": "# test"}
	b, err := json.Marshal(body)
	require.NoError(t, err)

	r := httptest.NewRequest(http.MethodPut, "/api/v1/kb/project/"+pid+"/file?path=concepts/test.md", bytes.NewReader(b))
	r.Header.Set("Content-Type", "text/plain")
	r.Header.Set("If-None-Match", "*")
	r.Header.Set("Authorization", "Bearer bob-token")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	assert.Equal(t, http.StatusForbidden, w.Code)
}

func TestKBAuthz_Put_NodePrincipal_403(t *testing.T) {
	// A node principal with no echoed X-Horde-User has no write identity and
	// is denied — fail closed (KSP §9).
	_, h, pid := newKBAuthTestServer(t)
	r := httptest.NewRequest(http.MethodPut, "/api/v1/kb/project/"+pid+"/file?path=concepts/test.md",
		bytes.NewReader([]byte("# test")))
	r.Header.Set("Content-Type", "text/plain")
	r.Header.Set("If-None-Match", "*")
	r.Header.Set("Authorization", "Bearer cluster-secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	assert.Equal(t, http.StatusForbidden, w.Code)
}

func TestKBAuthz_Put_NodePrincipal_AttributedOwner_200(t *testing.T) {
	// A node forward (participant API write or a stage-2 watcher push) echoes
	// the writing user via X-Horde-User. The authority re-derives that user's
	// write authority from local config (KSP §9). alice owns the project.
	_, h, pid := newKBAuthTestServer(t)
	r := httptest.NewRequest(http.MethodPut, "/api/v1/kb/project/"+pid+"/file?path=concepts/attr.md",
		bytes.NewReader([]byte("# attributed")))
	r.Header.Set("Content-Type", "text/plain")
	r.Header.Set("If-None-Match", "*")
	r.Header.Set("Authorization", "Bearer cluster-secret")
	r.Header.Set("X-Horde-User", "alice")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestKBAuthz_Put_NodePrincipal_AttributedNonTeam_403(t *testing.T) {
	// A node forward attributed to a user without write authority is denied:
	// the echoed identity is subject to the scope's write authority, never
	// blanket-trusted (KSP §9). bob is not the owner/admin.
	_, h, pid := newKBAuthTestServer(t)
	r := httptest.NewRequest(http.MethodPut, "/api/v1/kb/project/"+pid+"/file?path=concepts/nope.md",
		bytes.NewReader([]byte("# nope")))
	r.Header.Set("Content-Type", "text/plain")
	r.Header.Set("If-None-Match", "*")
	r.Header.Set("Authorization", "Bearer cluster-secret")
	r.Header.Set("X-Horde-User", "bob")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	assert.Equal(t, http.StatusForbidden, w.Code)
}

func TestKBAuthz_Put_Admin_200(t *testing.T) {
	_, h, pid := newKBAuthTestServer(t)
	r := httptest.NewRequest(http.MethodPut, "/api/v1/kb/project/"+pid+"/file?path=concepts/admin.md",
		bytes.NewReader([]byte("# admin")))
	r.Header.Set("Content-Type", "text/plain")
	r.Header.Set("If-None-Match", "*")
	r.Header.Set("Authorization", "Bearer admin-token")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	assert.Equal(t, http.StatusOK, w.Code)
}

// --- Delete authz ---

func TestKBAuthz_Delete_NonTeam_403(t *testing.T) {
	_, h, pid := newKBAuthTestServer(t)
	r := httptest.NewRequest(http.MethodDelete, "/api/v1/kb/project/"+pid+"/file?path=concepts/index.md", nil)
	r.Header.Set("If-Match", `"placeholder"`)
	r.Header.Set("Authorization", "Bearer bob-token")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	assert.Equal(t, http.StatusForbidden, w.Code)
}

func TestKBAuthz_Delete_NodePrincipal_403(t *testing.T) {
	_, h, pid := newKBAuthTestServer(t)
	r := httptest.NewRequest(http.MethodDelete, "/api/v1/kb/project/"+pid+"/file?path=concepts/index.md", nil)
	r.Header.Set("If-Match", `"placeholder"`)
	r.Header.Set("Authorization", "Bearer cluster-secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	assert.Equal(t, http.StatusForbidden, w.Code)
}
