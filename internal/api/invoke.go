package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"

	"github.com/go-chi/chi/v5"
)

// invokeRequestBody is the body the node forwards to the agent subprocess.
// The node injects session_id (derived from the agent's active project) into
// the client's original body before proxying. When the agent has no active
// project, session_id is empty and the agent falls back to per-invocation
// sessions (each invoke is a fresh session, no conversation continuity).
type invokeRequestBody struct {
	Message      string `json:"message"`
	InvocationID string `json:"invocation_id,omitempty"`
	SessionID    string `json:"session_id,omitempty"`
}

// invokeAgent routes a POST /agents/{id}/invoke request to the right backend.
// For a native ADK agent it reverse-proxies the SSE stream from the agent
// subprocess's unix socket. For an AAP agent there is no socket; the node
// runs the AAP turn itself (via AAPInvoke) and writes the SSE stream from
// the returned events. Both paths produce the same SSE event shape
// (token/done/error with an id: field), so the TUI and other clients consume
// them identically.
func invokeAgent(srv invokeView) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")

		// Reject invokes on paused projects. Finished projects clear the
		// agent's active-project binding (FinishProject), so the agent
		// falls through to the no-project path (empty state) and is
		// invokable with per-invocation sessions — the "projects are
		// additive" decision. Only the paused state (which retains the
		// binding) gates invocation.
		state := srv.AgentProjectState(id)
		if state == "paused" {
			writeJSON(w, http.StatusConflict, errorResponse{
				Error: "project is paused",
			})
			return
		}

		// Branch on agent kind. AAP agents have no socket; the node runs
		// the turn and writes the SSE stream from AAPInvoke's events.
		if srv.IsAAPAgent(id) {
			invokeAAPAgent(srv, w, r, id)
			return
		}
		// A local ADK agent has a unix socket to reverse-proxy to.
		if srv.AgentSocket(id) != "" {
			invokeADKAgent(srv, w, r, id)
			return
		}
		// Not local: route to the node that hosts the agent, if known
		// (master → owning slave). Otherwise it's genuinely unknown.
		if addr, ok := srv.RemoteAgentNode(id); ok {
			invokeRemoteAgent(w, r, addr)
			return
		}
		writeJSON(w, http.StatusNotFound, errorResponse{Error: errAgentNotFound})
	}
}

// invokeRemoteAgent reverse-proxies the invoke to the node hosting the agent,
// streaming the SSE response back. The path (/api/v1/agents/{id}/invoke) and
// body are preserved; Last-Event-ID and other headers pass through, so resume
// works across the hop. FlushInterval -1 streams each write immediately.
func invokeRemoteAgent(w http.ResponseWriter, r *http.Request, addr string) {
	target := &url.URL{Scheme: "http", Host: addr}
	proxy := &httputil.ReverseProxy{
		Rewrite: func(req *httputil.ProxyRequest) {
			req.SetURL(target) // host+scheme only; preserves the incoming path + query
		},
		FlushInterval: -1,
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			writeJSON(w, http.StatusBadGateway, errorResponse{Error: "route to owning node: " + err.Error()})
		},
	}
	proxy.ServeHTTP(w, r)
}

// invokeADKAgent is the reverse-proxy path for native ADK agents.
func invokeADKAgent(srv invokeView, w http.ResponseWriter, r *http.Request, id string) {
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

// invokeAAPAgent runs an AAP turn and writes the SSE stream from the
// server's AAPInvoke event stream. The event shape (token/done/error with an
// id: field) matches the ADK reverse proxy, so clients consume both kinds
// identically. Last-Event-ID resume works against the per-invocation ring
// buffer the server keeps for AAP invocations.
func invokeAAPAgent(srv invokeView, w http.ResponseWriter, r *http.Request, id string) {
	var req invokeRequestBody
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			// Tolerate an empty body (EOF) — treat as an empty message,
			// matching the ADK rewrite path.
			if err.Error() != "EOF" {
				writeJSON(w, http.StatusBadRequest, errorResponse{Error: errInvalidBody})
				return
			}
		}
	}
	sessionKey := srv.SessionKey(id)
	if sessionKey != "" {
		req.SessionID = sessionKey
	}

	flusher, _ := w.(http.Flusher)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	// Flush the SSE preamble immediately so the client's request returns as
	// soon as the stream is open, rather than blocking until the first event
	// (a turn may reach a tool-approval wait before producing any output).
	if flusher != nil {
		flusher.Flush()
	}

	events, errCh := srv.AAPInvoke(r.Context(), id, req.SessionID, req.InvocationID, req.Message)

	// Track the last written SSE id so a reconnecting client's Last-Event-ID
	// can resume from the buffer. The server's AAPInvoke replay path uses
	// the invocation id to find the buffer.
	lastID := parseLastEventID(r)
	for ev := range events {
		lastID++
		fmt.Fprintf(w, "id: %d\n", lastID) //#nosec G705 // SSE stream (text/event-stream); id is an integer counter, not HTML
		fmt.Fprintf(w, "event: %s\n", ev.Typ)
		fmt.Fprintf(w, "data: %s\n\n", ev.Data)
		if flusher != nil {
			flusher.Flush()
		}
	}
	if err := <-errCh; err != nil {
		// The terminal error event was already buffered into the stream by
		// the server (an "error" event). Reach here only if the stream was
		// cut before the error event was delivered; write a final inline
		// error so the client sees something.
		if !isContextCanceled(r, err) {
			errData, _ := json.Marshal(map[string]string{"error": err.Error()})
			lastID++
			fmt.Fprintf(w, "id: %d\n", lastID) //#nosec G705 // SSE stream (text/event-stream); id is an integer counter, not HTML
			fmt.Fprintf(w, "event: error\n")
			fmt.Fprintf(w, "data: %s\n\n", errData)
			if flusher != nil {
				flusher.Flush()
			}
		}
	}
}

// isContextCanceled reports whether err is the request's context cancellation
// (a client disconnect), so the handler can suppress a spurious error event
// on a clean disconnect.
func isContextCanceled(r *http.Request, err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, r.Context().Err())
}

// parseLastEventID extracts the Last-Event-ID header as an integer, mirroring
// the agentapi subprocess's parser. Returns 0 if absent or unparseable; a
// reconnecting client resumes from the AAP invocation's ring buffer at that id.
func parseLastEventID(r *http.Request) int {
	v := r.Header.Get("Last-Event-ID")
	if v == "" {
		return 0
	}
	id, err := strconv.Atoi(v)
	if err != nil {
		return 0
	}
	return id
}

// rewriteInvokeBody reads the client's request body, injects session_id,
// and returns the re-marshaled body as a reader. An empty body is treated
// as an empty message (no error) so that SSE reconnects with no body
// succeed. An unparseable non-empty body returns an error.
func rewriteInvokeBody(r *http.Request, sessionID string) (*bytes.Reader, error) {
	var req invokeRequestBody
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			// Tolerate an empty body (EOF) — treat as an empty message.
			if err.Error() != "EOF" {
				return nil, err
			}
		}
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
