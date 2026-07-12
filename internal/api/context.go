package api

import (
	"encoding/json"
	"net"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/geoffjay/horde/internal/server"
)

// fullContextAllowed reports whether the caller may see un-redacted execution
// context. A local (loopback) principal always may; a remote principal may
// only when the node opts in via agent.context_share = "full". This is the
// 3.5a node-granular principal seam: origin, not per-user identity.
func fullContextAllowed(r *http.Request, srv agentView) bool {
	return isLoopbackRequest(r) || srv.ContextShareFull()
}

// isLoopbackRequest reports whether the request originates from the local host.
func isLoopbackRequest(r *http.Request) bool {
	host := r.RemoteAddr
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// projectContext returns ctx as-is when the full view is allowed, otherwise a
// redacted copy safe for a remote principal.
func projectContext(ctx *server.ExecutionContext, full bool) *server.ExecutionContext {
	if full {
		return ctx
	}
	red := ctx.Redacted()
	return &red
}

// getAgentContext returns the execution context snapshot for one agent.
func getAgentContext(srv agentView) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		ctx := srv.AgentContext(id)
		if ctx == nil {
			writeJSON(w, http.StatusNotFound, errorResponse{Error: errAgentNotFound})
			return
		}
		writeJSON(w, http.StatusOK, projectContext(ctx, fullContextAllowed(r, srv)))
	}
}

// streamAgentContext streams execution context changes for one agent over
// SSE. The first event is the current snapshot, followed by deltas.
func streamAgentContext(srv agentView) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		if srv.AgentContext(id) == nil {
			writeJSON(w, http.StatusNotFound, errorResponse{Error: errAgentNotFound})
			return
		}

		// Subscribe before snapshotting so a change occurring between the
		// snapshot and the subscription is not lost (it stays queued on ch).
		ch, cancel := srv.SubscribeAgentContext(id)
		defer cancel()

		flusher, _ := w.(http.Flusher)
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.WriteHeader(http.StatusOK)

		full := fullContextAllowed(r, srv)

		// Send the current snapshot; any change since subscribing is still
		// queued on ch and delivered by the loop below.
		if ctx := srv.AgentContext(id); ctx != nil {
			writeContextSSE(w, flusher, projectContext(ctx, full))
		}

		for {
			select {
			case <-r.Context().Done():
				return
			case c := <-ch:
				writeContextSSE(w, flusher, projectContext(&c, full))
			}
		}
	}
}

// listAgentContexts returns the execution contexts of all local agents,
// redacted for a remote principal unless the node shares full context.
func listAgentContexts(srv agentView) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctxs := srv.AllAgentContexts()
		if !fullContextAllowed(r, srv) {
			for i := range ctxs {
				ctxs[i] = ctxs[i].Redacted()
			}
		}
		if ctxs == nil {
			ctxs = []server.ExecutionContext{}
		}
		writeJSON(w, http.StatusOK, ctxs)
	}
}

// listRemoteAgentContexts returns the aggregated, redacted execution
// contexts from all slaves. Served by the master only.
func listRemoteAgentContexts(srv clusterView) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctxs := srv.RemoteAgentContexts()

		// Optional ?issue= filter.
		if issue := r.URL.Query().Get("issue"); issue != "" {
			filtered := make([]server.ExecutionContext, 0)
			for i := range ctxs {
				if ctxs[i].Issue == issue {
					filtered = append(filtered, ctxs[i])
				}
			}
			ctxs = filtered
		}

		if ctxs == nil {
			ctxs = []server.ExecutionContext{}
		}
		writeJSON(w, http.StatusOK, ctxs)
	}
}

// writeContextSSE writes one SSE event containing the execution context
// and flushes.
func writeContextSSE(w http.ResponseWriter, flusher http.Flusher, ctx *server.ExecutionContext) {
	data, err := json.Marshal(ctx)
	if err != nil {
		return
	}
	if _, err := w.Write([]byte("event: context\n")); err != nil {
		return
	}
	if _, err := w.Write([]byte("data: ")); err != nil {
		return
	}
	if _, err := w.Write(data); err != nil {
		return
	}
	if _, err := w.Write([]byte("\n\n")); err != nil {
		return
	}
	if flusher != nil {
		flusher.Flush()
	}
}
