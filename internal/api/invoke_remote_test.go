package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/geoffjay/horde/internal/server"
)

// TestInvoke_RoutesToOwningNode asserts the master reverse-proxies an invoke
// for an agent hosted on a slave to that slave and streams its SSE back.
func TestInvoke_RoutesToOwningNode(t *testing.T) {
	// A fake slave that serves the agent's invoke as an SSE stream.
	var gotPath string
	slave := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl, _ := w.(http.Flusher)
		_, _ = io.WriteString(w, "id: 1\nevent: token\ndata: {\"text\":\"from-slave\"}\n\n")
		if fl != nil {
			fl.Flush()
		}
		_, _ = io.WriteString(w, "id: 2\nevent: done\ndata: {}\n\n")
		if fl != nil {
			fl.Flush()
		}
	}))
	defer slave.Close()

	// Master that knows the slave and that "remote-1" runs on it.
	srv := newTestServer(t)
	srv.RegisterSlave("slave-1", slave.Listener.Addr().String())
	srv.ReportContexts("slave-1", []server.ExecutionContext{{AgentID: "remote-1"}})
	h := Router(srv)

	w := do(t, h, http.MethodPost, "/api/v1/agents/remote-1/invoke",
		map[string]string{"message": "hi"})

	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "/api/v1/agents/remote-1/invoke", gotPath, "path is preserved across the hop")
	body := w.Body.String()
	assert.Contains(t, body, "from-slave", "the slave's streamed event should come back")
	assert.Contains(t, body, "event: done")
}

// TestInvoke_UnknownAgentIs404 asserts an id that is neither local nor a known
// remote agent still 404s.
func TestInvoke_UnknownAgentIs404(t *testing.T) {
	srv := newTestServer(t)
	h := Router(srv)
	w := do(t, h, http.MethodPost, "/api/v1/agents/ghost/invoke",
		map[string]string{"message": "hi"})
	assert.Equal(t, http.StatusNotFound, w.Code)
}

// TestInvoke_SlaveForwardsToLeader asserts that a slave receiving an invoke for
// an agent it does not host forwards it to the master (any node is a valid
// invoke entry point).
func TestInvoke_SlaveForwardsToLeader(t *testing.T) {
	// Fake master: a mux so the slave's background register/heartbeat POSTs
	// don't interfere with the invoke-path assertion.
	var gotPath string
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/agents/", func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl, _ := w.(http.Flusher)
		_, _ = io.WriteString(w, "id: 1\nevent: token\ndata: {\"text\":\"from-master\"}\n\n")
		if fl != nil {
			fl.Flush()
		}
		_, _ = io.WriteString(w, "id: 2\nevent: done\ndata: {}\n\n")
		if fl != nil {
			fl.Flush()
		}
	})
	mux.HandleFunc("/api/v1/cluster/", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})
	master := httptest.NewServer(mux)
	defer master.Close()

	srv, err := server.New(server.Config{
		Mode:              server.ModeSlave,
		Leader:            master.Listener.Addr().String(),
		SpawnDefaultAgent: false,
	})
	require.NoError(t, err)
	require.NoError(t, srv.Start(t.Context()))
	h := Router(srv)

	w := do(t, h, http.MethodPost, "/api/v1/agents/ghost/invoke",
		map[string]string{"message": "hi"})

	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "/api/v1/agents/ghost/invoke", gotPath, "the invoke is forwarded to the leader unchanged")
	assert.Contains(t, w.Body.String(), "from-master", "the master's streamed event should come back")
}
