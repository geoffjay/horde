package api

import (
	"context"

	"github.com/geoffjay/horde/internal/server"
)

// errInvalidBody is the error message returned when a request body fails to
// decode as JSON.
const errInvalidBody = "invalid request body"

// nodeView is the subset of *server.Server that node-control handlers need.
// Defined as an interface so handlers can be tested with a fake.
type nodeView interface {
	Mode() server.Mode
	LeaderConnected() bool
	NodeID() string
}

// agentView is the subset of *server.Server that agent handlers need.
type agentView interface {
	Agents() []server.AgentInfo
	SpawnAgent(ctx context.Context, name string) (string, error)
	StopAgent(id string) error
	AgentSocket(id string) string
	AgentContext(id string) *server.ExecutionContext
	AllAgentContexts() []server.ExecutionContext
	SubscribeAgentContext(id string) (<-chan server.ExecutionContext, func())
	ContextShareFull() bool
}

// clusterView is the subset of *server.Server that cluster handlers need.
type clusterView interface {
	Mode() server.Mode
	NodeID() string
	RegisterSlave(nodeID, addr string)
	Heartbeat(nodeID string, agents []string, digests []server.ExecutionContextDigest) (leaderID string, ok bool)
	Slaves() []server.SlaveInfo
	RemoteAgentContexts() []server.ExecutionContext
}

// projectView is the subset of *server.Server that project handlers need.
type projectView interface {
	CreateProject(ctx context.Context, in server.CreateProjectInput) (*server.Project, error)
	GetProject(id string) (*server.Project, error)
	ListProjects(stateFilter string) []server.Project
	PauseProject(id string) (*server.Project, error)
	ResumeProject(id string) (*server.Project, error)
	FinishProject(id string) (*server.Project, error)
	AssignAgent(ctx context.Context, projectID, agentName string) (*server.Project, error)
	RemoveAgentFromProject(projectID, agentID string) (*server.Project, error)
	AgentActiveProject(agentID string) string
	SessionKey(agentID string) string
}

// invokeView is the subset needed by the invoke proxy (extends agentView
// with session-key derivation and project-state checking).
type invokeView interface {
	agentView
	SessionKey(agentID string) string
	AgentProjectState(agentID string) string
}

// compile-time: *server.Server satisfies the handler interfaces.
var (
	_ nodeView    = (*server.Server)(nil)
	_ agentView   = (*server.Server)(nil)
	_ clusterView = (*server.Server)(nil)
	_ projectView = (*server.Server)(nil)
	_ invokeView  = (*server.Server)(nil)
)
