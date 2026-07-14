package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestListProjects_Empty(t *testing.T) {
	srv := newTestServer(t)
	h := Router(srv)

	w := do(t, h, http.MethodGet, "/api/v1/projects/", nil)
	require.Equal(t, http.StatusOK, w.Code)
	var projects []projectDTO
	require.NoError(t, json.NewDecoder(w.Body).Decode(&projects))
	assert.Empty(t, projects)
}

func TestCreateProject_RequiresName(t *testing.T) {
	srv := newTestServer(t)
	h := Router(srv)

	w := do(t, h, http.MethodPost, "/api/v1/projects/", createProjectRequest{
		AgentNames: []string{"greeter"},
	})
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestCreateProject_RequiresAgents(t *testing.T) {
	srv := newTestServer(t)
	h := Router(srv)

	w := do(t, h, http.MethodPost, "/api/v1/projects/", createProjectRequest{
		Name: "test",
	})
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestCreateProject_InvalidBody(t *testing.T) {
	srv := newTestServer(t)
	h := Router(srv)

	r := httptest.NewRequest(http.MethodPost, "/api/v1/projects/", bytes.NewReader([]byte("not json")))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestGetProject_NotFound(t *testing.T) {
	srv := newTestServer(t)
	h := Router(srv)

	w := do(t, h, http.MethodGet, "/api/v1/projects/nope", nil)
	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestAssignAgent_RequiresName(t *testing.T) {
	srv := newTestServer(t)
	h := Router(srv)

	w := do(t, h, http.MethodPost, "/api/v1/projects/nope/agents", assignAgentRequest{})
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestRemoveAgentFromProject_NotFound(t *testing.T) {
	srv := newTestServer(t)
	h := Router(srv)

	w := do(t, h, http.MethodDelete, "/api/v1/projects/nope/agents/a1", nil)
	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestPauseProject_NotFound(t *testing.T) {
	srv := newTestServer(t)
	h := Router(srv)

	w := do(t, h, http.MethodPost, "/api/v1/projects/nope/pause", nil)
	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestResumeProject_NotFound(t *testing.T) {
	srv := newTestServer(t)
	h := Router(srv)

	w := do(t, h, http.MethodPost, "/api/v1/projects/nope/resume", nil)
	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestFinishProject_NotFound(t *testing.T) {
	srv := newTestServer(t)
	h := Router(srv)

	w := do(t, h, http.MethodPost, "/api/v1/projects/nope/finish", nil)
	assert.Equal(t, http.StatusNotFound, w.Code)
}
