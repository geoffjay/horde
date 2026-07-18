package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/geoffjay/horde/internal/aap"
	"github.com/geoffjay/horde/internal/server"
)

// errAgentNotFound is the error message for an unknown agent id.
const errAgentNotFound = "agent not found"

// errInvalidDecision is returned when an approval decision is neither allow
// nor deny.
const errInvalidDecision = `decision must be "allow" or "deny"`

// agentDTO is the JSON shape for an agent in API responses.
type agentDTO struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Status  string `json:"status"`
	Healthy bool   `json:"healthy"`
}

// createAgentRequest is the body of POST /api/v1/agents. Node is an optional
// placement target: "" or "local" spawns on this node (unchanged behavior),
// "auto" asks the master to pick the least-loaded node, and a slave node id
// places the agent on that slave. Remote placement is master-only.
type createAgentRequest struct {
	Name string `json:"name"`
	Node string `json:"node,omitempty"`
}

// listAgents returns all currently registered agent subprocesses.
func listAgents(srv agentView) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		agents := srv.Agents()
		out := make([]agentDTO, 0, len(agents))
		for _, a := range agents {
			out = append(out, toAgentDTO(a))
		}
		writeJSON(w, http.StatusOK, out)
	}
}

// availableAgentDTO is the JSON shape for a spawnable agent type.
type availableAgentDTO struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
}

// listAvailableAgents returns the agent types this node can spawn (built-in
// ADK agents plus configured AAP definitions), so clients can offer a choice
// instead of a free-text name.
func listAvailableAgents(srv agentView) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		avail := srv.AvailableAgents()
		out := make([]availableAgentDTO, 0, len(avail))
		for _, a := range avail {
			out = append(out, availableAgentDTO{Name: a.Name, Kind: string(a.Kind)})
		}
		writeJSON(w, http.StatusOK, out)
	}
}

// createAgent spawns a new agent subprocess from the request body's name.
func createAgent(srv agentView) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req createAgentRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: errInvalidBody})
			return
		}
		if req.Name == "" {
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: errNameRequired})
			return
		}

		// Resolve placement. A local target spawns here as before; a remote
		// target forwards the spawn to the owning slave (master → node) and
		// relays its response, so the id the slave assigns reaches the caller.
		addr, local, err := srv.ResolveSpawnTarget(req.Node)
		if err != nil {
			status := http.StatusBadRequest
			if errors.Is(err, server.ErrNodeNotFound) {
				status = http.StatusNotFound
			}
			writeJSON(w, status, errorResponse{Error: err.Error()})
			return
		}
		if !local {
			forwardSpawn(srv, w, r, addr, req.Name)
			return
		}

		id, err := srv.SpawnAgent(context.Background(), req.Name)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, errorResponse{Error: err.Error()})
			return
		}
		// Reflect the freshly-spawned agent. srv.Agents() is a snapshot;
		// the new proc may not be visible yet under contention, so fall
		// back to a synthesized DTO.
		dto := agentDTO{ID: id, Name: req.Name, Status: string(server.AgentRunning), Healthy: true}
		for _, a := range srv.Agents() {
			if a.ID == id {
				dto = toAgentDTO(a)
				break
			}
		}
		writeJSON(w, http.StatusCreated, dto)
	}
}

// forwardSpawn relays a spawn to the slave hosting the placement target and
// copies its response (status, headers, body) back to the caller. The slave's
// local agents endpoint does the actual spawn and returns the agent DTO with
// the id it assigned; that id becomes routable for invoke once the slave's
// next heartbeat reaches the master.
func forwardSpawn(srv agentView, w http.ResponseWriter, r *http.Request, addr, name string) {
	status, header, body, err := srv.ForwardSpawn(r.Context(), addr, name)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, errorResponse{Error: "forward spawn to node: " + err.Error()})
		return
	}
	for k, vs := range header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// getAgent returns a single agent by id.
func getAgent(srv agentView) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		for _, a := range srv.Agents() {
			if a.ID == id {
				writeJSON(w, http.StatusOK, toAgentDTO(a))
				return
			}
		}
		writeJSON(w, http.StatusNotFound, errorResponse{Error: errAgentNotFound})
	}
}

// deleteAgent stops a single agent by id.
func deleteAgent(srv agentView) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		if err := srv.StopAgent(id); err != nil {
			if errors.Is(err, server.ErrAgentNotFound) {
				writeJSON(w, http.StatusNotFound, errorResponse{Error: errAgentNotFound})
				return
			}
			writeJSON(w, http.StatusInternalServerError, errorResponse{Error: err.Error()})
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// respondApprovalRequest is the body of
// POST /api/v1/agents/{id}/approvals/{requestID}.
type respondApprovalRequest struct {
	Decision string `json:"decision"`
}

// respondApproval resolves a pending tool-use approval for an AAP agent with
// an allow/deny decision — the node-as-approval-authority decision path. On
// success it returns the updated agent execution context (the pending ref is
// now cleared).
func respondApproval(srv agentView) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		requestID := chi.URLParam(r, "requestID")

		var req respondApprovalRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: errInvalidBody})
			return
		}
		var decision aap.ApprovalDecision
		switch req.Decision {
		case string(aap.DecisionAllow):
			decision = aap.DecisionAllow
		case string(aap.DecisionDeny):
			decision = aap.DecisionDeny
		default:
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: errInvalidDecision})
			return
		}

		if err := srv.RespondApproval(id, requestID, decision); err != nil {
			switch {
			case errors.Is(err, server.ErrAgentNotFound), errors.Is(err, server.ErrApprovalNotFound):
				writeJSON(w, http.StatusNotFound, errorResponse{Error: err.Error()})
			case errors.Is(err, server.ErrNotAAPAgent):
				writeJSON(w, http.StatusConflict, errorResponse{Error: err.Error()})
			default:
				writeJSON(w, http.StatusInternalServerError, errorResponse{Error: err.Error()})
			}
			return
		}

		ctx := srv.AgentContext(id)
		if ctx == nil {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		writeJSON(w, http.StatusOK, projectContext(ctx, fullContextAllowed(r, srv)))
	}
}

// toAgentDTO converts a server.AgentInfo to its JSON DTO.
func toAgentDTO(a server.AgentInfo) agentDTO {
	status := string(a.Status)
	if status == "" {
		status = string(server.AgentRunning)
	}
	return agentDTO{ID: a.ID, Name: a.Name, Status: status, Healthy: a.Healthy}
}

// errorResponse is a generic error envelope.
type errorResponse struct {
	Error string `json:"error"`
}
