package api

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCreateAgent_PlacesOnNode asserts the coordinator forwards a node-targeted
// spawn to that worker's agents endpoint and relays the worker's response
// (including the id it assigned).
func TestCreateAgent_PlacesOnNode(t *testing.T) {
	var gotPath, gotName string
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		gotName = body["name"]
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"id":"a7-42","name":"greeter","status":"running","healthy":true}`)
	}))
	defer worker.Close()

	srv := newTestServer(t)
	srv.RegisterWorker("worker-1", worker.Listener.Addr().String())
	h := Router(srv)

	w := do(t, h, http.MethodPost, "/api/v1/agents",
		createAgentRequest{Name: "greeter", Node: "worker-1"})

	require.Equal(t, http.StatusCreated, w.Code)
	assert.Equal(t, "/api/v1/agents", gotPath, "spawn is forwarded to the worker's agents endpoint")
	assert.Equal(t, "greeter", gotName, "the agent name is forwarded")

	var dto agentDTO
	require.NoError(t, json.NewDecoder(w.Body).Decode(&dto))
	assert.Equal(t, "a7-42", dto.ID, "the worker-assigned id is relayed to the caller")
	assert.Equal(t, "greeter", dto.Name)
}

// TestCreateAgent_UnknownNodeIs404 asserts a placement targeting an unknown
// node is rejected before any spawn happens.
func TestCreateAgent_UnknownNodeIs404(t *testing.T) {
	srv := newTestServer(t)
	h := Router(srv)

	w := do(t, h, http.MethodPost, "/api/v1/agents",
		createAgentRequest{Name: "greeter", Node: "no-such-node"})
	assert.Equal(t, http.StatusNotFound, w.Code)
}
