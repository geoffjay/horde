package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newFakeNode(t *testing.T) (*httptest.Server, *nodeStub) {
	t.Helper()
	stub := &nodeStub{}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/health", stub.health)
	mux.HandleFunc("/api/v1/node", stub.node)
	mux.HandleFunc("/api/v1/agents/", stub.agents) // subtree: covers /agents and /agents/{id}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, stub
}

type nodeStub struct {
	agentList []Agent
}

func (s *nodeStub) health(w http.ResponseWriter, _ *http.Request) {
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (s *nodeStub) node(w http.ResponseWriter, _ *http.Request) {
	_ = json.NewEncoder(w).Encode(NodeInfo{Mode: "master", LeaderConnected: true, NodeID: "n1", Version: "test"})
}

func (s *nodeStub) agents(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		_ = json.NewEncoder(w).Encode(s.agentList)
	case http.MethodPost:
		var req map[string]string
		_ = json.NewDecoder(r.Body).Decode(&req)
		a := Agent{ID: "agent-1", Name: req["name"], Status: "running"}
		s.agentList = append(s.agentList, a)
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(a)
	case http.MethodDelete:
		w.WriteHeader(http.StatusNoContent)
	}
}

func TestNew_NormalizesAddr(t *testing.T) {
	c := New("localhost:13420")
	assert.Equal(t, "http://localhost:13420", c.BaseURL())

	c = New("http://localhost:13420/")
	assert.Equal(t, "http://localhost:13420", c.BaseURL())
}

func TestHealth_Success(t *testing.T) {
	srv, _ := newFakeNode(t)
	c := New(srv.Listener.Addr().String())
	require.NoError(t, c.Health(context.Background()))
}

func TestHealth_Failure(t *testing.T) {
	c := New("127.0.0.1:1") // nothing listening
	err := c.Health(context.Background())
	require.Error(t, err)
}

func TestNode(t *testing.T) {
	srv, _ := newFakeNode(t)
	c := New(srv.Listener.Addr().String())
	n, err := c.Node(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "master", n.Mode)
	assert.True(t, n.LeaderConnected)
	assert.Equal(t, "n1", n.NodeID)
}

func TestListAgents(t *testing.T) {
	srv, stub := newFakeNode(t)
	stub.agentList = []Agent{{ID: "a1", Name: "greeter", Status: "running"}}
	c := New(srv.Listener.Addr().String())
	agents, err := c.ListAgents(context.Background())
	require.NoError(t, err)
	require.Len(t, agents, 1)
	assert.Equal(t, "greeter", agents[0].Name)
}

func TestSpawnAndStopAgent(t *testing.T) {
	srv, _ := newFakeNode(t)
	c := New(srv.Listener.Addr().String())

	a, err := c.SpawnAgent(context.Background(), "greeter")
	require.NoError(t, err)
	assert.Equal(t, "greeter", a.Name)

	require.NoError(t, c.StopAgent(context.Background(), a.ID))
}
