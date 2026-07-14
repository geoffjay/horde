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

// fakeForwarder is a projectForwarder that proxies to an httptest master.
// It is used to test the projectForwardMiddleware without needing a real
// slave-to-master cluster setup.
type fakeForwarder struct {
	leaderAddr string
	master     *httptest.Server
}

func (f *fakeForwarder) LeaderAddr() string { return f.leaderAddr }

func (f *fakeForwarder) ForwardProjectRequest(ctx context.Context, method, path string, body []byte) (int, http.Header, []byte, error) {
	url := f.master.URL + path
	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(body))
	if err != nil {
		return 0, nil, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, nil, nil, err
	}
	defer resp.Body.Close()
	respBody := make([]byte, 0, 1024)
	buf := make([]byte, 1024)
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			respBody = append(respBody, buf[:n]...)
		}
		if readErr != nil {
			break
		}
	}
	return resp.StatusCode, resp.Header, respBody, nil
}

// newMasterStub returns an httptest server that responds to project API
// requests with a fixed project list. It records the requests it receives
// so tests can assert forwarding behaviour.
func newMasterStub(t *testing.T) *httptest.Server {
	t.Helper()
	projects := []projectDTO{
		{ID: "p1", Name: "auth-service", State: "active", Goal: "Fix login", Team: teamDTO{Agents: []teamAgentDTO{{AgentID: "a1", Name: "greeter"}}}},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/projects/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodGet:
			if r.URL.Path == "/api/v1/projects/" {
				_ = json.NewEncoder(w).Encode(projects)
				return
			}
			_ = json.NewEncoder(w).Encode(projects[0])
			return
		case http.MethodPost:
			var req createProjectRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			p := projectDTO{ID: "p2", Name: req.Name, State: "active"}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(p)
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// routerWithForwarder builds a Router that uses the given forwarder for
// project routes. The non-project routes use the real server (master mode).
func routerWithForwarder(t *testing.T, fwd projectForwarder) http.Handler {
	t.Helper()
	srv := newTestServer(t)
	r := Router(srv)
	// Wrap the router to inject the forwarder via context is not possible
	// with chi's current setup. Instead, test the middleware directly.
	return r
}

func TestProjectForwardMiddleware_PassesThroughWhenNoLeader(t *testing.T) {
	srv := newTestServer(t)
	h := Router(srv)

	// Master mode: no forwarding, local handler responds.
	w := do(t, h, http.MethodGet, "/api/v1/projects/", nil)
	require.Equal(t, http.StatusOK, w.Code)
	var projects []projectDTO
	require.NoError(t, json.NewDecoder(w.Body).Decode(&projects))
	assert.Empty(t, projects, "master with no projects returns empty list")
}

func TestProjectForwardMiddleware_ForwardsToMaster(t *testing.T) {
	master := newMasterStub(t)
	fwd := &fakeForwarder{leaderAddr: master.Listener.Addr().String(), master: master}

	// Build a handler that wraps the project route with the forwarding
	// middleware. We test the middleware in isolation: when LeaderAddr is
	// set, it forwards and never calls the local handler.
	localSrv := newTestServer(t)
	localHandler := createProject(localSrv)

	mw := projectForwardMiddleware(fwd)(localHandler)

	// POST a project — should be forwarded to the master stub.
	body, _ := json.Marshal(createProjectRequest{
		Name: "billing", AgentNames: []string{"greeter"},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/projects/", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mw.ServeHTTP(w, req)

	require.Equal(t, http.StatusCreated, w.Code)
	var p projectDTO
	require.NoError(t, json.NewDecoder(w.Body).Decode(&p))
	assert.Equal(t, "billing", p.Name, "response should come from the master stub")
}

func TestProjectForwardMiddleware_ForwardsListToMaster(t *testing.T) {
	master := newMasterStub(t)
	fwd := &fakeForwarder{leaderAddr: master.Listener.Addr().String(), master: master}

	localSrv := newTestServer(t)
	localHandler := listProjects(localSrv)

	mw := projectForwardMiddleware(fwd)(localHandler)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/projects/", nil)
	w := httptest.NewRecorder()
	mw.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var projects []projectDTO
	require.NoError(t, json.NewDecoder(w.Body).Decode(&projects))
	require.Len(t, projects, 1)
	assert.Equal(t, "auth-service", projects[0].Name, "list should come from the master")
}

func TestProjectForwardMiddleware_NoLeaderPassesThrough(t *testing.T) {
	fwd := &fakeForwarder{leaderAddr: ""} // no leader

	localSrv := newTestServer(t)
	localHandler := listProjects(localSrv)

	mw := projectForwardMiddleware(fwd)(localHandler)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/projects/", nil)
	w := httptest.NewRecorder()
	mw.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var projects []projectDTO
	require.NoError(t, json.NewDecoder(w.Body).Decode(&projects))
	assert.Empty(t, projects, "local handler responds when no leader configured")
}

func TestProjectForwardMiddleware_LeaderErrorReturnsBadGateway(t *testing.T) {
	// Forwarder that always errors.
	fwd := &fakeForwarder{
		leaderAddr: "unreachable:1",
		master:     httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})),
	}
	defer fwd.master.Close()

	localSrv := newTestServer(t)
	localHandler := listProjects(localSrv)

	mw := projectForwardMiddleware(fwd)(localHandler)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/projects/", nil)
	w := httptest.NewRecorder()
	mw.ServeHTTP(w, req)

	// The fakeForwarder tries to dial the master URL (not the leader addr),
	// so it actually succeeds. To test the error path, use a forwarder
	// whose master is closed.
	fwd.master.Close()
	req2 := httptest.NewRequest(http.MethodGet, "/api/v1/projects/", nil)
	w2 := httptest.NewRecorder()
	mw.ServeHTTP(w2, req2)

	assert.Equal(t, http.StatusBadGateway, w2.Code)
}

func TestServer_LeaderAddr_MasterMode(t *testing.T) {
	srv, err := server.New(server.Config{SpawnDefaultAgent: false})
	require.NoError(t, err)
	require.NoError(t, srv.Start(context.Background()))
	assert.Empty(t, srv.LeaderAddr(), "master mode has no leader address")
}

func TestServer_LeaderAddr_SlaveWithoutLeader(t *testing.T) {
	srv, err := server.New(server.Config{Mode: server.ModeSlave, SpawnDefaultAgent: false})
	require.NoError(t, err)
	require.NoError(t, srv.Start(context.Background()))
	assert.Empty(t, srv.LeaderAddr(), "slave with no leader config has empty address")
}
