// Package client is a thin HTTP client for the horde node API. It is used
// by the TUI (and any other out-of-process consumer) to talk to a running
// node over the /api/v1 surface. The TUI is just another API client: it
// never imports internal/server and never starts a node in-process.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Client is an HTTP client for the horde node API. It targets one address at a
// time but can hold several cluster member addresses: on a transport failure
// (the node is unreachable, e.g. after a leader crash under raft failover) a
// unary request rotates to the next known member and retries, so the client
// follows leadership across a failover without external infrastructure. The
// member set is seeded at construction and expanded from cluster-view responses.
type Client struct {
	mu      sync.Mutex
	members []string // candidate base URLs; members[active] is the current target
	active  int

	http       *http.Client
	streamHTTP *http.Client
}

// httpTimeout is the default per-request timeout for client calls.
const httpTimeout = 10 * time.Second

// New constructs a client for the node at addr (host:port). A scheme prefix
// is optional; "http://" is assumed when absent.
func New(addr string) *Client {
	return NewCluster([]string{addr})
}

// NewCluster constructs a client seeded with several cluster member addresses.
// Unary requests start at the first and rotate to the others on transport
// failure. Empty/blank addresses are dropped; a client with no members targets
// nothing until members are learned.
func NewCluster(addrs []string) *Client {
	members := make([]string, 0, len(addrs))
	for _, a := range addrs {
		if n := normalizeAddr(a); n != "" {
			members = append(members, n)
		}
	}
	return &Client{
		members:    members,
		http:       &http.Client{Timeout: httpTimeout},
		streamHTTP: &http.Client{}, // no timeout: SSE streams live until closed
	}
}

// normalizeAddr trims whitespace, prepends http:// when no scheme is present,
// and strips a trailing slash. Returns "" for a blank address.
func normalizeAddr(addr string) string {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return ""
	}
	if !strings.HasPrefix(addr, "http://") && !strings.HasPrefix(addr, "https://") {
		addr = "http://" + addr
	}
	return strings.TrimRight(addr, "/")
}

// BaseURL returns the fully-qualified base URL the client currently targets
// (e.g. "http://localhost:13420"), for display in the TUI.
func (c *Client) BaseURL() string { return c.currentBase() }

// currentBase returns the current target base URL, or "" if the client has no
// members.
func (c *Client) currentBase() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.members) == 0 {
		return ""
	}
	return c.members[c.active]
}

// memberCount returns the number of known members.
func (c *Client) memberCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.members)
}

// rotate advances the current target to the next known member (wrapping).
func (c *Client) rotate() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.members) > 0 {
		c.active = (c.active + 1) % len(c.members)
	}
}

// Members returns a snapshot of the known member base URLs.
func (c *Client) Members() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, len(c.members))
	copy(out, c.members)
	return out
}

// mergeMembers adds any new addresses to the member set, preserving the current
// target. It is fed by cluster-view responses so the client learns the topology.
func (c *Client) mergeMembers(addrs []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	seen := make(map[string]bool, len(c.members))
	for _, m := range c.members {
		seen[m] = true
	}
	for _, a := range addrs {
		n := normalizeAddr(a)
		if n != "" && !seen[n] {
			c.members = append(c.members, n)
			seen[n] = true
		}
	}
}

// send issues a unary request, rotating to the next known member and retrying on
// a transport error (an unreachable node), up to one attempt per member. An HTTP
// response (any status) is returned as-is; only transport failures trigger a
// retry. The caller closes the response body.
func (c *Client) send(ctx context.Context, method, path, contentType string, body []byte) (*http.Response, error) {
	attempts := c.memberCount()
	if attempts == 0 {
		return nil, fmt.Errorf("no cluster members configured")
	}
	var lastErr error
	for i := 0; i < attempts; i++ {
		base := c.currentBase()
		var rdr io.Reader = http.NoBody
		if body != nil {
			rdr = bytes.NewReader(body)
		}
		req, err := http.NewRequestWithContext(ctx, method, base+path, rdr)
		if err != nil {
			return nil, err
		}
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		resp, err := c.http.Do(req)
		if err == nil {
			return resp, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		// The node is unreachable — try another member.
		c.rotate()
	}
	return nil, lastErr
}

// NodeInfo is the GET /api/v1/node response shape.
type NodeInfo struct {
	Mode            string `json:"mode"`
	LeaderConnected bool   `json:"leader_connected"`
	NodeID          string `json:"node_id"`
	Version         string `json:"version"`
}

// Agent is an agent entry in list/get responses.
type Agent struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
}

// Health pings GET /api/v1/health. It is the liveness probe the TUI uses to
// detect whether a node is reachable before showing its UI. Returns nil
// when the node responds with status "ok".
func (c *Client) Health(ctx context.Context) error {
	resp, err := c.get(ctx, "/api/v1/health")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("health: %s", resp.Status)
	}
	return nil
}

// Node fetches node metadata.
func (c *Client) Node(ctx context.Context) (NodeInfo, error) {
	var n NodeInfo
	if err := c.getJSON(ctx, "/api/v1/node", &n); err != nil {
		return n, err
	}
	return n, nil
}

// ListAgents fetches all running agents.
func (c *Client) ListAgents(ctx context.Context) ([]Agent, error) {
	var a []Agent
	if err := c.getJSON(ctx, "/api/v1/agents", &a); err != nil {
		return nil, err
	}
	return a, nil
}

// AvailableAgent is a spawnable agent type: a built-in ADK agent or a
// configured AAP agent definition.
type AvailableAgent struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
}

// ListAvailableAgents fetches the agent types this node can spawn.
func (c *Client) ListAvailableAgents(ctx context.Context) ([]AvailableAgent, error) {
	var a []AvailableAgent
	if err := c.getJSON(ctx, "/api/v1/agents/available", &a); err != nil {
		return nil, err
	}
	return a, nil
}

// SpawnAgent starts a new agent subprocess of the given name. node selects
// placement: "" or "local" spawns on the target node, "auto" lets the master
// pick the least-loaded node, and a node id places it on that node (master
// only). node is omitted from the request when empty.
func (c *Client) SpawnAgent(ctx context.Context, name, node string) (Agent, error) {
	var a Agent
	payload := map[string]string{"name": name}
	if node != "" {
		payload["node"] = node
	}
	body, _ := json.Marshal(payload)
	if err := c.postJSON(ctx, "/api/v1/agents", body, &a); err != nil {
		return a, err
	}
	return a, nil
}

// StopAgent stops an agent by id.
func (c *Client) StopAgent(ctx context.Context, id string) error {
	resp, err := c.send(ctx, http.MethodDelete, "/api/v1/agents/"+id, "", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("stop agent: %s", resp.Status)
	}
	return nil
}

// get issues a GET and returns the raw response (caller closes the body).
func (c *Client) get(ctx context.Context, path string) (*http.Response, error) {
	return c.send(ctx, http.MethodGet, path, "", nil)
}

// getJSON issues a GET and decodes a JSON response into out.
func (c *Client) getJSON(ctx context.Context, path string, out any) error {
	resp, err := c.get(ctx, path)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s", path, resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// postJSON issues a POST with a JSON body and decodes a JSON response.
func (c *Client) postJSON(ctx context.Context, path string, body []byte, out any) error {
	resp, err := c.send(ctx, http.MethodPost, path, "application/json", body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%s: %s", path, resp.Status)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
