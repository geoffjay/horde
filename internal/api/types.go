package api

import (
	"context"
	"net/http"

	"github.com/geoffjay/horde/internal/aap"
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
	// RespondApproval resolves a pending AAP tool-use approval with an
	// allow/deny decision (node-as-approval-authority).
	RespondApproval(agentID, requestID string, decision aap.ApprovalDecision) error
	// ResolveSpawnTarget maps a requested placement node to a concrete
	// target. local=true means spawn on this node; otherwise addr is the
	// slave address to forward the spawn to (master → owning node).
	ResolveSpawnTarget(requested string) (addr string, local bool, err error)
	// ForwardSpawn posts a spawn to a slave's agents endpoint and relays its
	// response (status, headers, body) — including the id the slave assigned.
	ForwardSpawn(ctx context.Context, addr, name string) (int, http.Header, []byte, error)
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

// projectForwarder is the subset of *server.Server needed to proxy project
// requests to the master. A slave node with a leader returns a non-empty
// LeaderAddr; the API layer forwards project reads and mutations to the
// master via ForwardProjectRequest.
type projectForwarder interface {
	LeaderAddr() string
	ForwardProjectRequest(ctx context.Context, method, path string, body []byte) (int, http.Header, []byte, error)
}

// invokeView is the subset needed by the invoke proxy (extends agentView
// with session-key derivation, project-state checking, and AAP streaming).
type invokeView interface {
	agentView
	SessionKey(agentID string) string
	AgentProjectState(agentID string) string
	// AAPInvoke runs one AAP turn against the agent's adapter session and
	// returns a stream of SSE-shaped events. It is used when the agent is an
	// AAP agent (no unix socket to reverse-proxy). The events channel is
	// closed when the turn is done; the err channel delivers a terminal
	// error (nil for a normal turn_complete). invocationID drives
	// Last-Event-ID resume against the per-invocation ring buffer.
	AAPInvoke(ctx context.Context, agentID, sessionKey, invocationID, message string) (<-chan server.AAPStreamEvent, <-chan error)
	// IsAAPAgent reports whether the agent is an AAP adapter (vs a native
	// ADK agent). The invoke handler branches on this: ADK uses the
	// reverse proxy; AAP uses AAPInvoke.
	IsAAPAgent(agentID string) bool
	// RemoteAgentNode resolves an agent id hosted on another node to that
	// node's reachable address, for cross-node invoke routing. ok is false
	// for a local/unknown/stale/ambiguous id. Consulted only when the agent
	// is not local.
	RemoteAgentNode(agentID string) (string, bool)
}

// compile-time: *server.Server satisfies the handler interfaces.
var (
	_ nodeView         = (*server.Server)(nil)
	_ agentView        = (*server.Server)(nil)
	_ clusterView      = (*server.Server)(nil)
	_ projectView      = (*server.Server)(nil)
	_ projectForwarder = (*server.Server)(nil)
	_ invokeView       = (*server.Server)(nil)
)
