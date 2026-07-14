package server

import (
	"context"
	"fmt"

	"github.com/sirupsen/logrus"
)

// CreateProject creates a new project, spawns its agents, and assigns them.
// At least one agent name is required.
func (s *Server) CreateProject(_ context.Context, in CreateProjectInput) (*Project, error) {
	p, err := s.projects.Create(in)
	if err != nil {
		return nil, err
	}

	// Spawn and assign each agent.
	for i := range p.Team.Agents {
		ta := &p.Team.Agents[i]
		agentID, err := s.SpawnAgent(context.Background(), ta.Name)
		if err != nil {
			logrus.WithError(err).WithFields(logrus.Fields{
				"project": p.ID, logKeyAgent: ta.Name,
			}).Warn("failed to spawn agent for project")
			continue
		}
		ta.AgentID = agentID
		s.assignAgentToProject(agentID, p.ID)
	}

	return p, nil
}

// GetProject returns a project by id.
func (s *Server) GetProject(id string) (*Project, error) {
	return s.projects.Get(id)
}

// ListProjects returns all projects, optionally filtered by state.
func (s *Server) ListProjects(stateFilter string) []Project {
	return s.projects.List(ProjectState(stateFilter))
}

// PauseProject transitions a project to the paused state.
func (s *Server) PauseProject(id string) (*Project, error) {
	return s.projects.UpdateState(id, ProjectPaused)
}

// ResumeProject transitions a project back to active.
func (s *Server) ResumeProject(id string) (*Project, error) {
	return s.projects.UpdateState(id, ProjectActive)
}

// FinishProject transitions a project to finished and clears agent bindings.
func (s *Server) FinishProject(id string) (*Project, error) {
	p, err := s.projects.UpdateState(id, ProjectFinished)
	if err != nil {
		return nil, err
	}

	// Clear active-project binding for all team agents.
	s.mu.Lock()
	for _, ta := range p.Team.Agents {
		if proc, ok := s.procs[ta.AgentID]; ok && proc.activeProject == id {
			proc.activeProject = ""
		}
	}
	s.mu.Unlock()

	return p, nil
}

// AssignAgent adds an agent to a project's team, spawns it if needed, and
// sets its active project.
func (s *Server) AssignAgent(_ context.Context, projectID, agentName string) (*Project, error) {
	p, err := s.projects.Get(projectID)
	if err != nil {
		return nil, err
	}

	// Check if the agent is already on the team and spawned.
	for _, ta := range p.Team.Agents {
		if ta.Name == agentName && ta.AgentID != "" {
			s.assignAgentToProject(ta.AgentID, projectID)
			return s.projects.Get(projectID)
		}
	}

	// Spawn the agent.
	agentID, err := s.SpawnAgent(context.Background(), agentName)
	if err != nil {
		return nil, fmt.Errorf("spawn agent %q: %w", agentName, err)
	}

	// Add to the project's team and set active project.
	updated, err := s.projects.AssignAgent(projectID, agentID, agentName)
	if err != nil {
		return nil, err
	}
	s.assignAgentToProject(agentID, projectID)
	return updated, nil
}

// RemoveAgentFromProject removes an agent from a project's team and clears
// its active-project binding if it was the active project.
func (s *Server) RemoveAgentFromProject(projectID, agentID string) (*Project, error) {
	updated, err := s.projects.RemoveAgent(projectID, agentID)
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	if proc, ok := s.procs[agentID]; ok && proc.activeProject == projectID {
		proc.activeProject = ""
	}
	s.mu.Unlock()

	return updated, nil
}

// AgentActiveProject returns the active project id for the given agent,
// or "" if the agent has no active project.
func (s *Server) AgentActiveProject(agentID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if proc, ok := s.procs[agentID]; ok {
		return proc.activeProject
	}
	return ""
}

// assignAgentToProject sets the agent's active project and seeds the
// execution context's project/issue fields.
func (s *Server) assignAgentToProject(agentID, projectID string) {
	s.mu.Lock()
	if proc, ok := s.procs[agentID]; ok {
		proc.activeProject = projectID
	}
	s.mu.Unlock()

	// Seed the execution context. The project's issue starts empty; the
	// agent may refine it via AAP context frames.
	s.ctxStore.setProject(agentID, projectID, "")
}

// SessionKey derives the session key for an agent from its active project.
// Returns "" when the agent has no active project (the caller falls back to
// Phase 3 per-invocation semantics).
func (s *Server) SessionKey(agentID string) string {
	projectID := s.AgentActiveProject(agentID)
	if projectID == "" {
		return ""
	}
	return agentID + ":" + projectID
}
