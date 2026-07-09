package api

import (
	"encoding/json"
	"net/http"
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
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid request body"})
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

// heartbeatResponse is the GET /api/v1/cluster/heartbeat response.
type heartbeatResponse struct {
	OK       bool   `json:"ok"`
	LeaderID string `json:"leader_id"`
}

// heartbeat handles a slave's periodic health check against this master.
func heartbeat(srv clusterView) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		nodeID := r.URL.Query().Get("node_id")
		if nodeID == "" {
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: "node_id is required"})
			return
		}
		leaderID, ok := srv.Heartbeat(nodeID)
		writeJSON(w, http.StatusOK, heartbeatResponse{OK: ok, LeaderID: leaderID})
	}
}
