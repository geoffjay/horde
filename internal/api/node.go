package api

import (
	"encoding/json"
	"net/http"
)

// nodeInfo is the GET /api/v1/node response.
type nodeInfo struct {
	Mode            string `json:"mode"`
	LeaderConnected bool   `json:"leader_connected"`
	NodeID          string `json:"node_id"`
	Version         string `json:"version"`
}

// version is set at link time via -ldflags; "dev" is the fallback.
var version = "dev"

// getNode returns node metadata: mode, leader connectivity, node id, and
// build version.
func getNode(srv nodeView) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, nodeInfo{
			Mode:            string(srv.Mode()),
			LeaderConnected: srv.LeaderConnected(),
			NodeID:          srv.NodeID(),
			Version:         version,
		})
	}
}

// healthResponse is the GET /api/v1/health response.
type healthResponse struct {
	Status string `json:"status"`
}

// getHealth is a dumb liveness check — the process is up. No dependencies.
func getHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, healthResponse{Status: "ok"})
}

// readyResponse is the GET /api/v1/ready response.
type readyResponse struct {
	Status string `json:"status"`
	Leader string `json:"leader"`
}

// getReady reports readiness. A master is always ready; a slave is ready
// when its leader connection is established, otherwise degraded.
func getReady(srv nodeView) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		leader := "ok"
		status := "ready"
		if srv.Mode() == "slave" && !srv.LeaderConnected() {
			leader = "degraded"
			status = "degraded"
		}
		writeJSON(w, http.StatusOK, readyResponse{Status: status, Leader: leader})
	}
}

// writeJSON encodes v as JSON to w with the given status code. On encoding
// failure it writes a 500 instead (the response is already partly sent, but
// this is a best-effort signal).
func writeJSON(w http.ResponseWriter, code int, v any) {
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		_, _ = w.Write([]byte(`{"error":"encode failed"}`))
	}
}
