package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"

	"github.com/go-chi/chi/v5"
)

// invokeRequestBody is the body the node forwards to the agent subprocess.
// The node injects session_id (derived from the agent's active project) into
// the client's original body before proxying. When the agent has no active
// project, session_id is empty and the agent falls back to per-invocation
// sessions (Phase 3 behavior).
type invokeRequestBody struct {
	Message      string `json:"message"`
	InvocationID string `json:"invocation_id,omitempty"`
	SessionID    string `json:"session_id,omitempty"`
}

// invokeAgent reverse-proxies the SSE stream from the agent subprocess's
// unix socket to the API client. The agent subprocess serves POST /invoke
// directly (see internal/agentapi); the node rewrites the path from
// /api/v1/agents/{id}/invoke to /invoke before proxying.
//
// The node reads the client's request body, injects session_id derived from
// the agent's active project, and re-marshals before forwarding. When the
// agent has no active project, session_id is omitted and the agent falls
// back to Phase 3 per-invocation sessions.
func invokeAgent(srv invokeView) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		socketPath := srv.AgentSocket(id)
		if socketPath == "" {
			writeJSON(w, http.StatusNotFound, errorResponse{Error: errAgentNotFound})
			return
		}

		body, err := rewriteInvokeBody(r, srv.SessionKey(id))
		if err != nil {
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: errInvalidBody})
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
				req.Out.Body = io.NopCloser(body)
				req.Out.ContentLength = int64(body.Len())
				req.Out.Header.Set("Content-Type", "application/json")
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

// rewriteInvokeBody reads the client's request body, injects session_id,
// and returns the re-marshaled body as a reader. If the original body is
// empty or unparseable, it returns an error.
func rewriteInvokeBody(r *http.Request, sessionID string) (*bytes.Reader, error) {
	var req invokeRequestBody
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return nil, err
	}
	if sessionID != "" {
		req.SessionID = sessionID
	}
	data, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	return bytes.NewReader(data), nil
}
