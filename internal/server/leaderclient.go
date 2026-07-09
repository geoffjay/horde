package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
)

// leaderClient is a thin HTTP client over the master node's cluster API.
// A slave uses it in connectLeader to register and then heartbeat.
type leaderClient struct {
	leader string
	nodeID string
	addr   string
	client *http.Client
}

// leaderClientTimeout is the per-request timeout for leader round-trips.
const leaderClientTimeout = 5 * time.Second

// newLeaderClient constructs a leader client. leader is the master address
// (host:port), nodeID is this slave's cluster id, addr is this slave's
// reachable address (optional, for the register payload).
func newLeaderClient(leader, nodeID, addr string) *leaderClient {
	leader = strings.TrimPrefix(leader, "http://")
	leader = strings.TrimPrefix(leader, "https://")
	return &leaderClient{
		leader: leader,
		nodeID: nodeID,
		addr:   addr,
		client: &http.Client{Timeout: leaderClientTimeout},
	}
}

// register calls POST /api/v1/cluster/register on the master. Returns the
// leader's node id on success.
func (c *leaderClient) register(ctx context.Context) (string, error) {
	body, err := json.Marshal(registerPayload{
		NodeID: c.nodeID,
		Mode:   string(ModeSlave),
		Addr:   c.addr,
	})
	if err != nil {
		return "", err
	}

	url := fmt.Sprintf("http://%s/api/v1/cluster/register", c.leader)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("register: leader returned %s", resp.Status)
	}

	var r registerResponse
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return "", err
	}
	return r.LeaderID, nil
}

// heartbeat calls GET /api/v1/cluster/heartbeat?node_id=<id> on the master.
func (c *leaderClient) heartbeat(ctx context.Context) error {
	url := fmt.Sprintf("http://%s/api/v1/cluster/heartbeat?node_id=%s", c.leader, c.nodeID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return err
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("heartbeat: leader returned %s", resp.Status)
	}
	return nil
}

// registerPayload mirrors the registerRequest shape in internal/api.
type registerPayload struct {
	NodeID string `json:"node_id"`
	Mode   string `json:"mode"`
	Addr   string `json:"addr"`
}

// registerResponse mirrors the registerResponse shape in internal/api.
type registerResponse struct {
	OK       bool   `json:"ok"`
	NodeID   string `json:"node_id"`
	LeaderID string `json:"leader_id"`
}

// keep logrus referenced for future logging in heartbeat loops.
var _ = logrus.Debug
