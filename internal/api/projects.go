package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/geoffjay/horde/internal/server"
)

// errNameRequired is the error message when a name field is missing.
const errNameRequired = "name is required"

// errProjectNotFound is the error message for an unknown project id.
const errProjectNotFound = "project not found"

// createProjectRequest is the body of POST /api/v1/projects.
type createProjectRequest struct {
	Name       string   `json:"name"`
	Workspace  string   `json:"workspace"`
	Goal       string   `json:"goal"`
	AgentNames []string `json:"agents"`
}

// teamAgentDTO is the JSON shape for a team agent member.
type teamAgentDTO struct {
	AgentID    string `json:"agent_id"`
	Name       string `json:"name"`
	AssignedAt string `json:"assigned_at"`
}

// teamDTO is the JSON shape for a team.
type teamDTO struct {
	Agents []teamAgentDTO `json:"agents"`
}

// projectDTO is the JSON shape for a project in API responses.
type projectDTO struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	Workspace string  `json:"workspace"`
	Goal      string  `json:"goal"`
	State     string  `json:"state"`
	Team      teamDTO `json:"team"`
}

// assignAgentRequest is the body of POST /api/v1/projects/{id}/agents. Exactly
// one of agent_id (attach an existing agent) or name (spawn a new agent by
// name) is used; agent_id takes precedence.
type assignAgentRequest struct {
	Name    string `json:"name"`
	AgentID string `json:"agent_id,omitempty"`
}

func toProjectDTO(p *server.Project) projectDTO {
	agents := make([]teamAgentDTO, 0, len(p.Team.Agents))
	for _, a := range p.Team.Agents {
		agents = append(agents, teamAgentDTO{
			AgentID:    a.AgentID,
			Name:       a.Name,
			AssignedAt: a.AssignedAt.Format("2006-01-02T15:04:05Z07:00"),
		})
	}
	return projectDTO{
		ID:        p.ID,
		Name:      p.Name,
		Workspace: p.Workspace,
		Goal:      p.Goal,
		State:     string(p.State),
		Team:      teamDTO{Agents: agents},
	}
}

// createProject creates a new project with a team of agents.
func createProject(srv projectView) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req createProjectRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: errInvalidBody})
			return
		}
		if req.Name == "" {
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: errNameRequired})
			return
		}
		if len(req.AgentNames) == 0 {
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: "at least one agent is required"})
			return
		}

		p, err := srv.CreateProject(r.Context(), server.CreateProjectInput{
			Name:       req.Name,
			Workspace:  req.Workspace,
			Goal:       req.Goal,
			AgentNames: req.AgentNames,
		})
		if err != nil {
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: err.Error()})
			return
		}
		writeJSON(w, http.StatusCreated, toProjectDTO(p))
	}
}

// listProjects returns all projects, optionally filtered by ?state=.
func listProjects(srv projectView) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		stateFilter := r.URL.Query().Get("state")
		projects := srv.ListProjects(stateFilter)
		out := make([]projectDTO, 0, len(projects))
		for i := range projects {
			out = append(out, toProjectDTO(&projects[i]))
		}
		writeJSON(w, http.StatusOK, out)
	}
}

// getProject returns a single project by id.
func getProject(srv projectView) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		p, err := srv.GetProject(id)
		if err != nil {
			if errors.Is(err, server.ErrProjectNotFound) {
				writeJSON(w, http.StatusNotFound, errorResponse{Error: errProjectNotFound})
				return
			}
			writeJSON(w, http.StatusInternalServerError, errorResponse{Error: err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, toProjectDTO(p))
	}
}

// pauseProject transitions a project to the paused state.
func pauseProject(srv projectView) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		p, err := srv.PauseProject(id)
		if err != nil {
			if errors.Is(err, server.ErrProjectNotFound) {
				writeJSON(w, http.StatusNotFound, errorResponse{Error: errProjectNotFound})
				return
			}
			writeJSON(w, http.StatusInternalServerError, errorResponse{Error: err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, toProjectDTO(p))
	}
}

// resumeProject transitions a project back to active.
func resumeProject(srv projectView) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		p, err := srv.ResumeProject(id)
		if err != nil {
			if errors.Is(err, server.ErrProjectNotFound) {
				writeJSON(w, http.StatusNotFound, errorResponse{Error: errProjectNotFound})
				return
			}
			writeJSON(w, http.StatusInternalServerError, errorResponse{Error: err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, toProjectDTO(p))
	}
}

// finishProject transitions a project to finished.
func finishProject(srv projectView) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		p, err := srv.FinishProject(id)
		if err != nil {
			if errors.Is(err, server.ErrProjectNotFound) {
				writeJSON(w, http.StatusNotFound, errorResponse{Error: errProjectNotFound})
				return
			}
			writeJSON(w, http.StatusInternalServerError, errorResponse{Error: err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, toProjectDTO(p))
	}
}

// assignAgentToProject assigns an agent to a project's team.
func assignAgentToProject(srv projectView) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		var req assignAgentRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: errInvalidBody})
			return
		}
		if req.AgentID == "" && req.Name == "" {
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: "agent_id or name is required"})
			return
		}

		// agent_id attaches an existing agent; name spawns a new one.
		var (
			p   *server.Project
			err error
		)
		if req.AgentID != "" {
			p, err = srv.AttachAgent(id, req.AgentID)
		} else {
			p, err = srv.AssignAgent(r.Context(), id, req.Name)
		}
		if err != nil {
			if errors.Is(err, server.ErrProjectNotFound) || errors.Is(err, server.ErrAgentNotFound) {
				writeJSON(w, http.StatusNotFound, errorResponse{Error: err.Error()})
				return
			}
			writeJSON(w, http.StatusInternalServerError, errorResponse{Error: err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, toProjectDTO(p))
	}
}

// removeAgentFromProject removes an agent from a project's team.
func removeAgentFromProject(srv projectView) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "id")
		agentID := chi.URLParam(r, "agentID")
		_, err := srv.RemoveAgentFromProject(projectID, agentID)
		if err != nil {
			if errors.Is(err, server.ErrProjectNotFound) {
				writeJSON(w, http.StatusNotFound, errorResponse{Error: errProjectNotFound})
				return
			}
			writeJSON(w, http.StatusInternalServerError, errorResponse{Error: err.Error()})
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}
