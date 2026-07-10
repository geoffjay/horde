package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/geoffjay/horde/internal/server"
)

// agentDTO is the JSON shape for an agent in API responses.
type agentDTO struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
}

// createAgentRequest is the body of POST /api/v1/agents.
type createAgentRequest struct {
	Name string `json:"name"`
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

// createAgent spawns a new agent subprocess from the request body's name.
func createAgent(srv agentView) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req createAgentRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: errInvalidBody})
			return
		}
		if req.Name == "" {
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: "name is required"})
			return
		}

		id, err := srv.SpawnAgent(r.Context(), req.Name)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, errorResponse{Error: err.Error()})
			return
		}
		// Reflect the freshly-spawned agent. srv.Agents() is a snapshot;
		// the new proc may not be visible yet under contention, so fall
		// back to a synthesized DTO.
		dto := agentDTO{ID: id, Name: req.Name, Status: string(server.AgentRunning)}
		for _, a := range srv.Agents() {
			if a.ID == id {
				dto = toAgentDTO(a)
				break
			}
		}
		writeJSON(w, http.StatusCreated, dto)
	}
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
		writeJSON(w, http.StatusNotFound, errorResponse{Error: "agent not found"})
	}
}

// deleteAgent stops a single agent by id.
func deleteAgent(srv agentView) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		if err := srv.StopAgent(id); err != nil {
			if errors.Is(err, server.ErrAgentNotFound) {
				writeJSON(w, http.StatusNotFound, errorResponse{Error: "agent not found"})
				return
			}
			writeJSON(w, http.StatusInternalServerError, errorResponse{Error: err.Error()})
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// toAgentDTO converts a server.AgentInfo to its JSON DTO.
func toAgentDTO(a server.AgentInfo) agentDTO {
	status := string(a.Status)
	if status == "" {
		status = string(server.AgentRunning)
	}
	return agentDTO{ID: a.ID, Name: a.Name, Status: status}
}

// errorResponse is a generic error envelope.
type errorResponse struct {
	Error string `json:"error"`
}
