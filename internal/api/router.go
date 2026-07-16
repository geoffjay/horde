// Package api implements the horde node HTTP API: an adapter that exposes
// the node core (internal/server) over HTTP/JSON with SSE for streaming.
//
// The API is versioned under /api/v1 and built on chi (a thin net/http
// router). Handlers call into *server.Server directly; this package owns no
// agent state itself. See docs/knowledgebase/decisions/http-api-transport.md
// for the transport decision and docs/knowledgebase/plans/phase-2-server-api.md
// for the full surface.
package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/geoffjay/horde/internal/server"
)

// Router builds the chi router for the node API, wiring all /api/v1 routes
// against the given server. It is the single entry point used by Server.Run's
// HTTP listener.
func Router(srv *server.Server) http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.Recoverer)
	r.Use(middleware.RequestID)
	r.Use(jsonContentType)

	r.Route("/api/v1", func(r chi.Router) {
		// Node control
		r.Get("/node", getNode(srv))
		r.Get("/health", getHealth)
		r.Get("/ready", getReady(srv))

		// Agents
		r.Get("/agents", listAgents(srv))
		r.Post("/agents", createAgent(srv))
		r.Get("/agents/context", listAgentContexts(srv))
		r.Get("/agents/{id}", getAgent(srv))
		r.Delete("/agents/{id}", deleteAgent(srv))
		r.Post("/agents/{id}/invoke", invokeAgent(srv))
		r.Get("/agents/{id}/context", getAgentContext(srv))
		r.Get("/agents/{id}/context/stream", streamAgentContext(srv))
		r.Post("/agents/{id}/approvals/{requestID}", respondApproval(srv))

		// Cluster (slave ↔ master)
		r.Post("/cluster/register", registerSlave(srv))
		r.Post("/cluster/heartbeat", heartbeat(srv))
		r.Get("/cluster/nodes", listNodes(srv))
		r.Get("/cluster/agents/context", listRemoteAgentContexts(srv))

		// Projects
		r.Route("/projects", func(r chi.Router) {
			// On a slave with a leader, forward all project requests to the
			// master. The master is the source of truth for project state.
			r.Use(projectForwardMiddleware(srv))
			r.Post("/", createProject(srv))
			r.Get("/", listProjects(srv))
			r.Get("/{id}", getProject(srv))
			r.Post("/{id}/pause", pauseProject(srv))
			r.Post("/{id}/resume", resumeProject(srv))
			r.Post("/{id}/finish", finishProject(srv))
			r.Post("/{id}/agents", assignAgentToProject(srv))
			r.Delete("/{id}/agents/{agentID}", removeAgentFromProject(srv))
		})
	})

	return r
}

// jsonContentType sets the default Content-Type for API responses. Handlers
// that stream (SSE) override it per-write.
func jsonContentType(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		next.ServeHTTP(w, r)
	})
}
