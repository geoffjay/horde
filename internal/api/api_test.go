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

func newTestServer(t *testing.T) *server.Server {
	t.Helper()
	srv, err := server.New(server.Config{SpawnDefaultAgent: false})
	require.NoError(t, err)
	require.NoError(t, srv.Start(context.Background()))
	t.Cleanup(func() {
		// stop any spawned procs
		for _, a := range srv.Agents() {
			_ = srv.StopAgent(a.ID)
		}
	})
	return srv
}

func do(t *testing.T, h http.Handler, method, target string, body any) *httptest.ResponseRecorder {
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
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestGetNode(t *testing.T) {
	srv := newTestServer(t)
	h := Router(srv, srv.EventBus())

	w := do(t, h, http.MethodGet, "/api/v1/node", nil)
	require.Equal(t, http.StatusOK, w.Code)
	var info nodeInfo
	require.NoError(t, json.NewDecoder(w.Body).Decode(&info))
	assert.Equal(t, "master", info.Mode)
	assert.True(t, info.LeaderConnected)
}

func TestGetHealth(t *testing.T) {
	srv := newTestServer(t)
	h := Router(srv, srv.EventBus())

	w := do(t, h, http.MethodGet, "/api/v1/health", nil)
	require.Equal(t, http.StatusOK, w.Code)
	var hr healthResponse
	require.NoError(t, json.NewDecoder(w.Body).Decode(&hr))
	assert.Equal(t, "ok", hr.Status)
}

func TestGetReady_Master(t *testing.T) {
	srv := newTestServer(t)
	h := Router(srv, srv.EventBus())

	w := do(t, h, http.MethodGet, "/api/v1/ready", nil)
	require.Equal(t, http.StatusOK, w.Code)
	var rr readyResponse
	require.NoError(t, json.NewDecoder(w.Body).Decode(&rr))
	assert.Equal(t, "ready", rr.Status)
	assert.Equal(t, "ok", rr.Leader)
}

func TestGetReady_SlaveDegraded(t *testing.T) {
	// A slave with no leader connection is not ready: /ready must report
	// degraded AND return 503 so status-gating orchestrators pull it from
	// rotation.
	srv, err := server.New(server.Config{Mode: server.ModeSlave, SpawnDefaultAgent: false})
	require.NoError(t, err)
	require.NoError(t, srv.Start(context.Background()))
	h := Router(srv, srv.EventBus())

	w := do(t, h, http.MethodGet, "/api/v1/ready", nil)
	require.Equal(t, http.StatusServiceUnavailable, w.Code)
	var rr readyResponse
	require.NoError(t, json.NewDecoder(w.Body).Decode(&rr))
	assert.Equal(t, "degraded", rr.Status)
	assert.Equal(t, "degraded", rr.Leader)
}

func TestListAgents_Empty(t *testing.T) {
	srv := newTestServer(t)
	h := Router(srv, srv.EventBus())

	w := do(t, h, http.MethodGet, "/api/v1/agents", nil)
	require.Equal(t, http.StatusOK, w.Code)
	var agents []agentDTO
	require.NoError(t, json.NewDecoder(w.Body).Decode(&agents))
	assert.Empty(t, agents)
}

func TestCreateAgent_RequiresName(t *testing.T) {
	srv := newTestServer(t)
	h := Router(srv, srv.EventBus())

	w := do(t, h, http.MethodPost, "/api/v1/agents", createAgentRequest{})
	require.Equal(t, http.StatusBadRequest, w.Code)
}

func TestCreateAgent_MissingBody(t *testing.T) {
	srv := newTestServer(t)
	h := Router(srv, srv.EventBus())

	r := httptest.NewRequest(http.MethodPost, "/api/v1/agents", bytes.NewReader([]byte("not json")))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	require.Equal(t, http.StatusBadRequest, w.Code)
}

func TestDeleteAgent_NotFound(t *testing.T) {
	srv := newTestServer(t)
	h := Router(srv, srv.EventBus())

	w := do(t, h, http.MethodDelete, "/api/v1/agents/agent-doesnotexist", nil)
	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestClusterRegister(t *testing.T) {
	srv := newTestServer(t)
	h := Router(srv, srv.EventBus())

	w := do(t, h, http.MethodPost, "/api/v1/cluster/register", registerRequest{
		NodeID: "slave-1", Mode: "slave", Addr: "slave1:13420",
	})
	require.Equal(t, http.StatusOK, w.Code)
	var rr registerResponse
	require.NoError(t, json.NewDecoder(w.Body).Decode(&rr))
	assert.True(t, rr.OK)
	assert.Equal(t, "slave-1", rr.NodeID)
}

func TestClusterRegister_RequiresNodeID(t *testing.T) {
	srv := newTestServer(t)
	h := Router(srv, srv.EventBus())

	w := do(t, h, http.MethodPost, "/api/v1/cluster/register", registerRequest{Mode: "slave"})
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestClusterHeartbeat(t *testing.T) {
	srv := newTestServer(t)
	h := Router(srv, srv.EventBus())

	w := do(t, h, http.MethodPost, "/api/v1/cluster/register", registerRequest{
		NodeID: "slave-1", Mode: "slave", Addr: "slave1:13420",
	})
	require.Equal(t, http.StatusOK, w.Code)

	w = do(t, h, http.MethodPost, "/api/v1/cluster/heartbeat", heartbeatRequest{
		NodeID: "slave-1", Agents: []string{"greeter"},
	})
	require.Equal(t, http.StatusOK, w.Code)
	var hb heartbeatResponse
	require.NoError(t, json.NewDecoder(w.Body).Decode(&hb))
	assert.True(t, hb.OK)
}

func TestClusterHeartbeat_RequiresNodeID(t *testing.T) {
	srv := newTestServer(t)
	h := Router(srv, srv.EventBus())

	w := do(t, h, http.MethodPost, "/api/v1/cluster/heartbeat", heartbeatRequest{Agents: []string{"x"}})
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestClusterNodes(t *testing.T) {
	srv := newTestServer(t)
	h := Router(srv, srv.EventBus())

	// Register a slave, then heartbeat with an agent list.
	w := do(t, h, http.MethodPost, "/api/v1/cluster/register", registerRequest{
		NodeID: "slave-1", Mode: "slave", Addr: "slave1:13420",
	})
	require.Equal(t, http.StatusOK, w.Code)
	w = do(t, h, http.MethodPost, "/api/v1/cluster/heartbeat", heartbeatRequest{
		NodeID: "slave-1", Agents: []string{"greeter"},
	})
	require.Equal(t, http.StatusOK, w.Code)

	w = do(t, h, http.MethodGet, "/api/v1/cluster/nodes", nil)
	require.Equal(t, http.StatusOK, w.Code)
	var resp clusterNodesResponse
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	require.Len(t, resp.Nodes, 1)
	assert.Equal(t, "slave-1", resp.Nodes[0].NodeID)
	assert.Equal(t, "slave1:13420", resp.Nodes[0].Addr)
	assert.Equal(t, []string{"greeter"}, resp.Nodes[0].Agents)
	assert.False(t, resp.Nodes[0].Stale)
	assert.NotEmpty(t, resp.Nodes[0].LastSeen)
}
