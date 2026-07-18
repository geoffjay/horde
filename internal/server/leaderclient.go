package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

// leaderClient is a thin HTTP client over the master node's cluster API.
// A slave uses it in connectLeader to register and then heartbeat. The leader
// address is resolved through a Discoverer on each call, so a dns-discovered
// leader that moves is picked up without a restart; the last resolved address
// is cached for leaderAddr().
type leaderClient struct {
	disco  Discoverer
	nodeID string
	addr   string
	token  string // shared cluster auth token (empty = disabled)
	client *http.Client

	mu     sync.Mutex
	leader string // last resolved leader address (cached for leaderAddr)
}

// leaderClientTimeout is the per-request timeout for leader round-trips.
const leaderClientTimeout = 5 * time.Second

// newLeaderClient constructs a leader client. disco resolves the master
// address, nodeID is this slave's cluster id, addr is this slave's reachable
// address (optional, for the register payload). A static discoverer seeds the
// cached address immediately so leaderAddr() is available before the first
// register; a dns discoverer resolves lazily in the background (no network in
// the constructor).
func newLeaderClient(disco Discoverer, nodeID, addr, token string) *leaderClient {
	c := &leaderClient{
		disco:  disco,
		nodeID: nodeID,
		addr:   addr,
		token:  token,
		client: &http.Client{Timeout: leaderClientTimeout},
	}
	if sd, ok := disco.(*staticDiscoverer); ok {
		c.leader = sd.addr
	}
	return c
}

// resolve asks the discoverer for the current leader address, caches it for
// leaderAddr(), and returns it.
func (c *leaderClient) resolve(ctx context.Context) (string, error) {
	addr, err := c.disco.Leader(ctx)
	if err != nil {
		return "", err
	}
	addr = normalizeAddr(addr)
	c.mu.Lock()
	c.leader = addr
	c.mu.Unlock()
	return addr, nil
}

// register calls POST /api/v1/cluster/register on the master. Returns the
// leader's node id on success.
func (c *leaderClient) register(ctx context.Context) (string, error) {
	leader, err := c.resolve(ctx)
	if err != nil {
		return "", err
	}

	body, err := json.Marshal(registerPayload{
		NodeID: c.nodeID,
		Mode:   string(ModeSlave),
		Addr:   c.addr,
	})
	if err != nil {
		return "", err
	}

	url := fmt.Sprintf("http://%s/api/v1/cluster/register", leader)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	SetClusterAuth(req.Header, c.token)

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
	leader, err := c.resolve(ctx)
	if err != nil {
		return err
	}

	body, err := json.Marshal(heartbeatPayload{
		NodeID:   c.nodeID,
		Agents:   agents,
		Contexts: digests,
	})
	if err != nil {
		return err
	}

	url := fmt.Sprintf("http://%s/api/v1/cluster/heartbeat", leader)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	SetClusterAuth(req.Header, c.token)

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

// leaderAddr returns the last resolved master address (host:port), or empty
// before the first successful resolve (e.g. a dns discoverer that has not yet
// looked up its SRV record).
func (c *leaderClient) leaderAddr() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.leader
}

// forwardRequest forwards an HTTP request to the master node, copying the
// response status, headers, and body back to the caller. It is used by slave
// nodes to proxy project reads and mutations to the master so project state
// is cluster-wide. The method and path are taken from the original request.
//
//nolint:gocritic // unnamedResult: result types are clear from context
func (c *leaderClient) forwardRequest(ctx context.Context, method, path string, body []byte) (int, http.Header, []byte, error) {
	leader, err := c.resolve(ctx)
	if err != nil {
		return 0, nil, nil, fmt.Errorf("resolve leader: %w", err)
	}
	url := fmt.Sprintf("http://%s%s", leader, path)
	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(body))
	if err != nil {
		return 0, nil, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	SetClusterAuth(req.Header, c.token)

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
