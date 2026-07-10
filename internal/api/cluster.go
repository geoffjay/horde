package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/geoffjay/horde/internal/server"
)

// registerRequest is the body of POST /api/v1/cluster/register.
type registerRequest struct {
	NodeID string `json:"node_id"`
	Mode   string `json:"mode"`
	Addr   string `json:"addr"`
}

// registerResponse is the POST /api/v1/cluster/register response.
type registerResponse struct {
	OK       bool   `json:"ok"`
	NodeID   string `json:"node_id"`
	LeaderID string `json:"leader_id"`
}

// registerSlave handles a slave registering with this master.
func registerSlave(srv clusterView) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req registerRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: errInvalidBody})
			return
		}
		if req.NodeID == "" {
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: "node_id is required"})
			return
		}
		srv.RegisterSlave(req.NodeID, req.Addr)
		writeJSON(w, http.StatusOK, registerResponse{
			OK:       true,
			NodeID:   req.NodeID,
			LeaderID: srv.NodeID(),
		})
	}
}

// heartbeatRequest is the body of POST /api/v1/cluster/heartbeat.
type heartbeatRequest struct {
	NodeID string   `json:"node_id"`
	Agents []string `json:"agents"`
}

// heartbeatResponse is the POST /api/v1/cluster/heartbeat response.
type heartbeatResponse struct {
	OK       bool   `json:"ok"`
	LeaderID string `json:"leader_id"`
}

// heartbeat handles a slave's periodic health check against this master,
// refreshing its last-seen time and reported agents.
func heartbeat(srv clusterView) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req heartbeatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: errInvalidBody})
			return
		}
		if req.NodeID == "" {
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: "node_id is required"})
			return
		}
		leaderID, ok := srv.Heartbeat(req.NodeID, req.Agents)
		writeJSON(w, http.StatusOK, heartbeatResponse{OK: ok, LeaderID: leaderID})
	}
}

// slaveDTO is the JSON shape for a registered slave in the cluster view.
type slaveDTO struct {
	NodeID   string   `json:"node_id"`
	Addr     string   `json:"addr"`
	Agents   []string `json:"agents"`
	LastSeen string   `json:"last_seen"`
	Stale    bool     `json:"stale"`
}

// clusterNodesResponse is the GET /api/v1/cluster/nodes response: the leader's
// id plus every slave registered with this master.
type clusterNodesResponse struct {
	LeaderID string     `json:"leader_id"`
	Nodes    []slaveDTO `json:"nodes"`
}

// listNodes returns the master's view of the cluster: the slaves that have
// registered and their last-seen/agents state. On a slave node the registry
// is empty, so this returns just the leader id and no nodes.
func listNodes(srv clusterView) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		slaves := srv.Slaves()
		nodes := make([]slaveDTO, 0, len(slaves))
		for i := range slaves {
			nodes = append(nodes, toSlaveDTO(&slaves[i]))
		}
		writeJSON(w, http.StatusOK, clusterNodesResponse{LeaderID: srv.NodeID(), Nodes: nodes})
	}
}

// toSlaveDTO converts a server.SlaveInfo to its JSON DTO.
func toSlaveDTO(s *server.SlaveInfo) slaveDTO {
	agents := s.Agents
	if agents == nil {
		agents = []string{}
	}
	var lastSeen string
	if !s.LastSeen.IsZero() {
		lastSeen = s.LastSeen.UTC().Format(time.RFC3339)
	}
	return slaveDTO{
		NodeID:   s.NodeID,
		Addr:     s.Addr,
		Agents:   agents,
		LastSeen: lastSeen,
		Stale:    s.Stale,
	}
}
