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
