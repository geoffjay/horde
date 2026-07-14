package server

import (
	"errors"
	"fmt"
	"sync"
	"time"
)

// ProjectState is the lifecycle state of a project.
type ProjectState string

const (
	// ProjectActive allows agent invocations.
	ProjectActive ProjectState = "active"
	// ProjectPaused suspends agent invocations.
	ProjectPaused ProjectState = "paused"
	// ProjectFinished releases agents and retains context for eviction.
	ProjectFinished ProjectState = "finished"
)

// TeamAgent is an agent member of a team.
type TeamAgent struct {
	AgentID    string    `json:"agent_id"`
	Name       string    `json:"name"`
	AssignedAt time.Time `json:"assigned_at"`
}

// TeamUser is a user member of a team. Reserved for 3.5b; empty in 3.5a.
type TeamUser struct {
	UserID string `json:"user_id"`
}

// Team is the set of users and agents assigned to a project.
type Team struct {
	Agents []TeamAgent `json:"agents"`
	Users  []TeamUser  `json:"users,omitempty"`
}

// Project is the unit of work: a workspace, a goal, a team, and a lifecycle.
type Project struct {
	ID        string       `json:"id"`
	Name      string       `json:"name"`
	Workspace string       `json:"workspace"`
	Goal      string       `json:"goal"`
	State     ProjectState `json:"state"`
	Team      Team         `json:"team"`
	CreatedAt time.Time    `json:"created_at"`
	UpdatedAt time.Time    `json:"updated_at"`
}

// ErrProjectNotFound is returned when a project id is unknown.
var ErrProjectNotFound = errors.New("project not found")

// ErrProjectNotActive is returned when an operation requires an active
// project but the project is paused or finished.
var ErrProjectNotActive = errors.New("project not active")

// ProjectStore persists project metadata and team composition. The v1
// implementation is in-memory (memProjectStore); a persistence backend
// (JSON flush or database) swaps in behind this interface without
// reshaping the server.
type ProjectStore interface {
	Create(in CreateProjectInput) (*Project, error)
	Get(id string) (*Project, error)
	List(stateFilter ProjectState) []Project
	UpdateState(id string, state ProjectState) (*Project, error)
	AssignAgent(id, agentID, agentName string) (*Project, error)
	RemoveAgent(id, agentID string) (*Project, error)
	Delete(id string) error
}

// CreateProjectInput is the input for creating a project.
type CreateProjectInput struct {
	Name       string
	Workspace  string
	Goal       string
	AgentNames []string
}

// memProjectStore is the in-memory ProjectStore implementation.
type memProjectStore struct {
	mu       sync.Mutex
	projects map[string]*Project
	nextID   int
	now      func() time.Time
}

func newProjectStore() ProjectStore {
	return &memProjectStore{
		projects: make(map[string]*Project),
		now:      time.Now,
	}
}

// Create creates a new project in the active state with the supplied team of
// agents. At least one agent name is required (a team is never empty).
func (ps *memProjectStore) Create(in CreateProjectInput) (*Project, error) {
	if in.Name == "" {
		return nil, errors.New("project name is required")
	}
	if len(in.AgentNames) == 0 {
		return nil, errors.New("at least one agent is required")
	}

	ps.mu.Lock()
	defer ps.mu.Unlock()

	ps.nextID++
	id := fmt.Sprintf("proj-%d", ps.nextID)
	now := ps.now().UTC()

	team := Team{Agents: make([]TeamAgent, 0, len(in.AgentNames))}
	for _, name := range in.AgentNames {
		team.Agents = append(team.Agents, TeamAgent{
			Name:       name,
			AssignedAt: now,
		})
	}

	p := &Project{
		ID:        id,
		Name:      in.Name,
		Workspace: in.Workspace,
		Goal:      in.Goal,
		State:     ProjectActive,
		Team:      team,
		CreatedAt: now,
		UpdatedAt: now,
	}
	ps.projects[id] = p
	return p, nil
}

// Get returns a copy of the project by id, or ErrProjectNotFound.
func (ps *memProjectStore) Get(id string) (*Project, error) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	p, ok := ps.projects[id]
	if !ok {
		return nil, ErrProjectNotFound
	}
	cp := *p
	return &cp, nil
}

// List returns copies of all projects, optionally filtered by state.
func (ps *memProjectStore) List(stateFilter ProjectState) []Project {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	out := make([]Project, 0, len(ps.projects))
	for _, p := range ps.projects {
		if stateFilter != "" && p.State != stateFilter {
			continue
		}
		out = append(out, *p)
	}
	return out
}

// UpdateState transitions a project's state.
func (ps *memProjectStore) UpdateState(id string, state ProjectState) (*Project, error) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	p, ok := ps.projects[id]
	if !ok {
		return nil, ErrProjectNotFound
	}
	p.State = state
	p.UpdatedAt = ps.now().UTC()
	cp := *p
	return &cp, nil
}

// AssignAgent adds an agent to the project's team. If the agent is already
// on the team, it is a no-op (returns the current project).
func (ps *memProjectStore) AssignAgent(id, agentID, agentName string) (*Project, error) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	p, ok := ps.projects[id]
	if !ok {
		return nil, ErrProjectNotFound
	}
	if p.State != ProjectActive {
		return nil, ErrProjectNotActive
	}

	for _, a := range p.Team.Agents {
		if a.AgentID == agentID {
			cp := *p
			return &cp, nil
		}
	}

	p.Team.Agents = append(p.Team.Agents, TeamAgent{
		AgentID:    agentID,
		Name:       agentName,
		AssignedAt: ps.now().UTC(),
	})
	p.UpdatedAt = ps.now().UTC()
	cp := *p
	return &cp, nil
}

// RemoveAgent removes an agent from the project's team.
func (ps *memProjectStore) RemoveAgent(id, agentID string) (*Project, error) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	p, ok := ps.projects[id]
	if !ok {
		return nil, ErrProjectNotFound
	}

	for i, a := range p.Team.Agents {
		if a.AgentID == agentID {
			p.Team.Agents = append(p.Team.Agents[:i], p.Team.Agents[i+1:]...)
			p.UpdatedAt = ps.now().UTC()
			break
		}
	}
	cp := *p
	return &cp, nil
}

// Delete removes a project from the store.
func (ps *memProjectStore) Delete(id string) error {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	if _, ok := ps.projects[id]; !ok {
		return ErrProjectNotFound
	}
	delete(ps.projects, id)
	return nil
}
