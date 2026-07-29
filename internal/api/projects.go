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

// teamUserDTO is the JSON shape for a team user member (3.5b).
type teamUserDTO struct {
	UserID string `json:"user_id"`
}

// teamDTO is the JSON shape for a team.
type teamDTO struct {
	Agents []teamAgentDTO `json:"agents"`
	Users  []teamUserDTO  `json:"users,omitempty"`
}

// projectDTO is the JSON shape for a project in API responses.
type projectDTO struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	Workspace string  `json:"workspace"`
	Goal      string  `json:"goal"`
	State     string  `json:"state"`
	Team      teamDTO `json:"team"`
	Owner     string  `json:"owner,omitempty"`
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
	users := make([]teamUserDTO, 0, len(p.Team.Users))
	for _, u := range p.Team.Users {
		users = append(users, teamUserDTO{UserID: u.UserID})
	}
	return projectDTO{
		ID:        p.ID,
		Name:      p.Name,
		Workspace: p.Workspace,
		Goal:      p.Goal,
		State:     string(p.State),
		Team:      teamDTO{Agents: agents, Users: users},
		Owner:     p.Owner,
	}
}

// createProject creates a new project with a team of agents.
func createProject(srv projectAuthView) http.HandlerFunc {
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

		// Attribute the project to the resolved user (empty when auth is
		// disabled — backward compatible). On a forwarded slave request the
		// X-Horde-User header supplies the originating user's id.
		owner := resolveOwnerForCreate(srv, r)

		p, err := srv.CreateProject(r.Context(), server.CreateProjectInput{
			Name:       req.Name,
			Workspace:  req.Workspace,
			Goal:       req.Goal,
			AgentNames: req.AgentNames,
			Owner:      owner,
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
func pauseProject(srv projectAuthView) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		if err := authorizeProject(srv, r, id, levelOwn); err != nil {
			writeAuthzError(w, err)
			return
		}
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
func resumeProject(srv projectAuthView) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		if err := authorizeProject(srv, r, id, levelOwn); err != nil {
			writeAuthzError(w, err)
			return
		}
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
func finishProject(srv projectAuthView) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		if err := authorizeProject(srv, r, id, levelOwn); err != nil {
			writeAuthzError(w, err)
			return
		}
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
func assignAgentToProject(srv projectAuthView) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		if err := authorizeProject(srv, r, id, levelOwn); err != nil {
			writeAuthzError(w, err)
			return
		}
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
func removeAgentFromProject(srv projectAuthView) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "id")
		agentID := chi.URLParam(r, "agentID")
		if err := authorizeProject(srv, r, projectID, levelOwn); err != nil {
			writeAuthzError(w, err)
			return
		}
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

// addProjectUserRequest is the body of POST /api/v1/projects/{id}/users.
type addProjectUserRequest struct {
	UserID string `json:"user_id"`
}

// addProjectUser adds a user to a project's team (3.5b). Owner-only.
func addProjectUser(srv projectAuthView) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "id")
		if err := authorizeProject(srv, r, projectID, levelOwn); err != nil {
			writeAuthzError(w, err)
			return
		}
		var req addProjectUserRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: errInvalidBody})
			return
		}
		if req.UserID == "" {
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: "user_id is required"})
			return
		}
		p, err := srv.AddUserToProject(projectID, req.UserID)
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

// removeProjectUser removes a user from a project's team (3.5b). Owner-only.
func removeProjectUser(srv projectAuthView) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "id")
		userID := chi.URLParam(r, "userID")
		if err := authorizeProject(srv, r, projectID, levelOwn); err != nil {
			writeAuthzError(w, err)
			return
		}
		_, err := srv.RemoveUserFromProject(projectID, userID)
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
