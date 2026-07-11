package api

import (
	"context"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"

	"github.com/go-chi/chi/v5"
)

// invokeAgent reverse-proxies the SSE stream from the agent subprocess's
// unix socket to the API client. The agent subprocess serves POST /invoke
// directly (see internal/agentapi); the node rewrites the path from
// /api/v1/agents/{id}/invoke to /invoke before proxying.
func invokeAgent(srv agentView) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		socketPath := srv.AgentSocket(id)
		if socketPath == "" {
			writeJSON(w, http.StatusNotFound, errorResponse{Error: errAgentNotFound})
			return
		}

		target := &url.URL{Scheme: "http", Host: "unix"}
		proxy := &httputil.ReverseProxy{
			// Rewrite the path: the agent subprocess serves /invoke,
			// not /api/v1/agents/{id}/invoke. We use Rewrite instead
			// of Director because Director is deprecated in Go 1.26+.
			Rewrite: func(req *httputil.ProxyRequest) {
				req.SetURL(target)
				req.Out.URL.Path = "/invoke"
				req.Out.URL.RawPath = ""
			},
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					d := net.Dialer{}
					return d.DialContext(ctx, "unix", socketPath) //#nosec G704 // server-controlled socket path
				},
			},
			// Flush every write immediately — critical for SSE streaming.
			FlushInterval: -1,
		}
		proxy.ServeHTTP(w, r)
	}
}
