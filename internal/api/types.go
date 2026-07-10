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
}

// clusterView is the subset of *server.Server that cluster handlers need.
type clusterView interface {
	Mode() server.Mode
	NodeID() string
	RegisterSlave(nodeID, addr string)
	Heartbeat(nodeID string, agents []string) (leaderID string, ok bool)
	Slaves() []server.SlaveInfo
}

// compile-time: *server.Server satisfies the handler interfaces.
var (
	_ nodeView    = (*server.Server)(nil)
	_ agentView   = (*server.Server)(nil)
	_ clusterView = (*server.Server)(nil)
)
