package server

import (
	"context"
	"fmt"

	"github.com/sirupsen/logrus"
)

// CreateProject creates a new project, spawns its agents, and assigns them.
// At least one agent name is required. When the create request omits the
// workspace, the configured default (project.workspace_dir) is used and
// written onto the project record so that GetProject, KB scaffolding, and
// agent spawning all agree on the same path.
func (s *Server) CreateProject(_ context.Context, in CreateProjectInput) (*Project, error) {
	// Resolve the default workspace once, before creating the project, so
	// the project record carries the resolved value.
	workspace := in.Workspace
	if workspace == "" {
		workspace = s.cfg.ProjectWorkspaceDir
	}
	in.Workspace = workspace

	p, err := s.projects.Create(in)
	if err != nil {
		return nil, err
	}

	// Scaffold the knowledgebase in the project workspace.
	if err := scaffoldKnowledgebase(workspace, p.Name); err != nil {
		logrus.WithError(err).WithFields(logrus.Fields{
			logKeyProject: p.ID, "workspace": workspace,
		}).Warn("failed to scaffold knowledgebase")
	}

	// Spawn and assign each agent, passing the same workspace path. Creation
	// is atomic: if any agent fails to spawn, roll back (stop the agents
	// already spawned and delete the project) and return the error. This
	// avoids leaving a project with an unspawnable team entry whose empty
	// agent id would later fail invoke with a bare 404.
	spawned := make([]string, 0, len(p.Team.Agents))
	for i := range p.Team.Agents {
		ta := &p.Team.Agents[i]
		agentID, err := s.spawnAgentWithWorkspace(context.Background(), ta.Name, workspace)
		if err != nil {
			s.rollbackProjectCreate(p.ID, spawned)
			return nil, fmt.Errorf("create project %q: agent %q failed to spawn: %w", p.Name, ta.Name, err)
		}
		ta.AgentID = agentID
		s.assignAgentToProject(agentID, p.ID)
		spawned = append(spawned, agentID)
	}

	return p, nil
}

// rollbackProjectCreate undoes a partially-created project after a spawn
// failure: it stops every agent already spawned for the project and deletes
// the project record, so a failed create leaves no orphaned agent subprocess
// or half-populated project behind.
func (s *Server) rollbackProjectCreate(projectID string, spawned []string) {
	for _, id := range spawned {
		if err := s.StopAgent(id); err != nil {
			logrus.WithError(err).WithField(logKeyAgent, id).Warn("rollback: stop agent failed")
		}
	}
	if err := s.projects.Delete(projectID); err != nil {
		logrus.WithError(err).WithField(logKeyProject, projectID).Warn("rollback: delete project failed")
	}
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
// Each agent's execution context is marked exited (triggering the Slice A
// retention path) and the active-project binding is cleared.
func (s *Server) FinishProject(id string) (*Project, error) {
	p, err := s.projects.UpdateState(id, ProjectFinished)
	if err != nil {
		return nil, err
	}

	// Clear active-project binding and mark context for retention/eviction
	// for all team agents.
	s.mu.Lock()
	agentIDs := make([]string, 0, len(p.Team.Agents))
	for _, ta := range p.Team.Agents {
		if proc, ok := s.procs[ta.AgentID]; ok && proc.activeProject == id {
			proc.activeProject = ""
		}
		agentIDs = append(agentIDs, ta.AgentID)
	}
	s.mu.Unlock()

	// Trigger context retention/eviction for each agent. The context store's
	// setLifecycle schedules eviction after the configured retention period.
	for _, agentID := range agentIDs {
		if ctx := s.ctxStore.get(agentID); ctx != nil {
			s.ctxStore.setLifecycle(agentID, AgentExited)
		}
	}

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
	agentID, err := s.spawnAgentWithWorkspace(context.Background(), agentName, p.Workspace)
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

// AgentProjectState returns the state of the agent's active project, or
// "" if the agent has no active project. Used by the invoke path to reject
// invokes on paused or finished projects.
func (s *Server) AgentProjectState(agentID string) string {
	projectID := s.AgentActiveProject(agentID)
	if projectID == "" {
		return ""
	}
	p, err := s.projects.Get(projectID)
	if err != nil {
		return ""
	}
	return string(p.State)
}

// ReassignAgent binds an already-spawned agent to a different project,
// removing it from its prior project's team and adding it to the new
// project's team. This is the reassignment path: the agent subprocess
// stays running, but its active project (and session key) changes.
func (s *Server) ReassignAgent(agentID, projectID string) {
	// Get the agent name for the team entry.
	s.mu.Lock()
	agentName := ""
	if proc, ok := s.procs[agentID]; ok {
		agentName = proc.name
	}
	s.mu.Unlock()

	// Remove from old project + set active + seed context.
	s.assignAgentToProject(agentID, projectID)

	// Add to the new project's team.
	if agentName != "" {
		if _, err := s.projects.AssignAgent(projectID, agentID, agentName); err != nil {
			logrus.WithError(err).WithFields(logrus.Fields{
				logKeyAgent: agentID, logKeyProject: projectID,
			}).Debug("agent already on project team or project not active")
		}
	}
}

// assignAgentToProject sets the agent's active project and seeds the
// execution context's project/issue fields. If the agent was active on a
// different project, it is removed from that project's team first
// (reassignment). Adding the agent to the new project's team is the
// caller's responsibility (CreateProject sets it during creation; the
// AssignAgent server method calls projects.AssignAgent).
func (s *Server) assignAgentToProject(agentID, projectID string) {
	// Check for a prior active project and remove the agent from its team.
	s.mu.Lock()
	oldProject := ""
	if proc, ok := s.procs[agentID]; ok {
		oldProject = proc.activeProject
		proc.activeProject = projectID
	}
	s.mu.Unlock()

	if oldProject != "" && oldProject != projectID {
		if _, err := s.projects.RemoveAgent(oldProject, agentID); err != nil {
			logrus.WithError(err).WithFields(logrus.Fields{
				logKeyAgent: agentID, logKeyProject: oldProject,
			}).Warn("failed to remove agent from prior project during reassignment")
		}
	}

	// Seed the execution context. The project's issue starts empty; the
	// agent may refine it via AAP context frames.
	s.ctxStore.setProject(agentID, projectID, "")
}

// SessionKey derives the session key for an agent from its active project.
// Returns "" when the agent has no active project (the caller falls back to
// per-invocation sessions with no conversation continuity).
func (s *Server) SessionKey(agentID string) string {
	projectID := s.AgentActiveProject(agentID)
	if projectID == "" {
		return ""
	}
	return agentID + ":" + projectID
}
