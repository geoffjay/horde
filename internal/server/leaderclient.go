package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
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

// heartbeat calls POST /api/v1/cluster/heartbeat on the master, reporting this
// slave's node id, its running agents, and their execution context digests.
func (c *leaderClient) heartbeat(ctx context.Context, agents []string, digests []ExecutionContextDigest) error {
	body, err := json.Marshal(heartbeatPayload{
		NodeID:   c.nodeID,
		Agents:   agents,
		Contexts: digests,
	})
	if err != nil {
		return err
	}

	url := fmt.Sprintf("http://%s/api/v1/cluster/heartbeat", c.leader)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

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

// leaderAddr returns the master address (host:port) or empty when no leader
// is configured.
func (c *leaderClient) leaderAddr() string { return c.leader }

// forwardRequest forwards an HTTP request to the master node, copying the
// response status, headers, and body back to the caller. It is used by slave
// nodes to proxy project reads and mutations to the master so project state
// is cluster-wide. The method and path are taken from the original request.
//
//nolint:gocritic // unnamedResult: result types are clear from context
func (c *leaderClient) forwardRequest(ctx context.Context, method, path string, body []byte) (int, http.Header, []byte, error) {
	url := fmt.Sprintf("http://%s%s", c.leader, path)
	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(body))
	if err != nil {
		return 0, nil, nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return 0, nil, nil, fmt.Errorf("forward to leader: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, nil, nil, fmt.Errorf("read leader response: %w", err)
	}
	return resp.StatusCode, resp.Header, respBody, nil
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

// heartbeatPayload mirrors the heartbeatRequest shape in internal/api.
type heartbeatPayload struct {
	NodeID   string                   `json:"node_id"`
	Agents   []string                 `json:"agents"`
	Contexts []ExecutionContextDigest `json:"contexts,omitempty"`
}

// ExecutionContextDigest is the redacted, wire-format context digest a slave
// sends to the master in the heartbeat payload.
type ExecutionContextDigest struct {
	AgentID              string        `json:"agent_id"`
	Project              string        `json:"project,omitempty"`
	Issue                string        `json:"issue,omitempty"`
	Activity             ActivityState `json:"activity"`
	WaitingModel         bool          `json:"waiting_model"`
	Blocked              bool          `json:"blocked"`
	ErrorCount           int           `json:"error_count,omitempty"`
	PendingApprovalCount int           `json:"pending_approval_count,omitempty"`
	Lifecycle            AgentState    `json:"lifecycle"`
	UpdatedAt            time.Time     `json:"updated_at"`
}
