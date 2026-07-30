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
	r.Use(resolvePrincipal(srv))
	r.Use(jsonContentType)

	r.Route("/api/v1", func(r chi.Router) {
		// Node control
		r.Get("/node", getNode(srv))
		r.Get("/health", getHealth)
		r.Get("/ready", getReady(srv))

		// Agents — reads stay open (origin-redacted as 3.5a).
		r.Get("/agents", listAgents(srv))
		r.Get("/agents/available", listAvailableAgents(srv))
		r.Get("/agents/context", listAgentContexts(srv))
		r.Get("/agents/{id}", getAgent(srv))
		r.Get("/agents/{id}/context", getAgentContext(srv))
		r.Get("/agents/{id}/context/stream", streamAgentContext(srv))
		// Agent mutations: requireUser rejects anonymous mutations when
		// auth is enabled (disabled ⇒ no-op; node ⇒ pass for cross-node
		// traffic). The handler does the actual work — this is the
		// anonymous-gate layer, not project-level authz.
		r.Group(func(r chi.Router) {
			r.Use(requireUser(srv))
			r.Post("/agents", createAgent(srv))
			r.Delete("/agents/{id}", deleteAgent(srv))
			r.Post("/agents/{id}/invoke", invokeAgent(srv))
			r.Post("/agents/{id}/approvals/{requestID}", respondApproval(srv))
		})

		// Cluster (worker ↔ coordinator). The node→node ingest endpoints require the
		// shared cluster auth token (when configured); the read endpoints below
		// are also used by local clients (the TUI) and stay open.
		r.With(requireClusterAuth(srv)).Post("/cluster/register", registerWorker(srv))
		r.With(requireClusterAuth(srv)).Post("/cluster/heartbeat", heartbeat(srv))
		r.With(requireClusterAuth(srv)).Post("/cluster/events", receiveClusterEvent(srv))
		r.Get("/cluster/nodes", listNodes(srv))
		r.Get("/cluster/agents/context", listRemoteAgentContexts(srv))

		// Cluster-activity event stream (SSE)
		r.Get("/events/stream", streamEvents(srv))

		// Users (per-user auth; ids only — never tokens)
		r.Get("/users", listUsers(srv))

		// Projects. The coordinator is the source of truth for project state; a
		// worker with a leader forwards project requests to it
		// (projectForwardMiddleware).
		r.Route("/projects", func(r chi.Router) {
			// Reads are open (origin-redacted) and forward-only on a worker.
			r.Group(func(r chi.Router) {
				r.Use(projectForwardMiddleware(srv))
				r.Get("/", listProjects(srv))
				r.Get("/{id}", getProject(srv))
			})
			// Mutations: requireUser gates BEFORE the forward, so a worker
			// rejects an anonymous mutation at the edge instead of forwarding
			// it to the coordinator as trusted node traffic (disabled ⇒ no-op).
			// The coordinator then enforces ownership (authorizeProject),
			// re-deriving the forwarded user from X-Horde-User.
			r.Group(func(r chi.Router) {
				r.Use(requireUser(srv))
				r.Use(projectForwardMiddleware(srv))
				r.Post("/", createProject(srv))
				r.Post("/{id}/pause", pauseProject(srv))
				r.Post("/{id}/resume", resumeProject(srv))
				r.Post("/{id}/finish", finishProject(srv))
				r.Post("/{id}/agents", assignAgentToProject(srv))
				r.Delete("/{id}/agents/{agentID}", removeAgentFromProject(srv))
				// Team membership (3.5b): owner-only add/remove user.
				r.Post("/{id}/users", addProjectUser(srv))
				r.Delete("/{id}/users/{userID}", removeProjectUser(srv))
			})
		})

		// Knowledgebase sync (KSP v1). Scope-keyed routes sit outside the
		// forwarded /projects group by construction: a participant serves
		// its local copy rather than forwarding to the leader. Disabled
		// (the default) ⇒ 501 with X-KSP-Enabled: false.
		r.Route("/kb", func(r chi.Router) {
			r.Get("/{kind}/{id}/manifest", getKBManifest(srv))
			r.Get("/{kind}/{id}/file", getKBFile(srv))
			r.Put("/{kind}/{id}/file", putKBFile(srv))
			r.Delete("/{kind}/{id}/file", deleteKBFile(srv))
			r.Get("/{kind}/{id}/conflicts", getKBConflicts(srv))
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
