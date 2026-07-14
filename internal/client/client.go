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
	"net/http"
	"strings"
	"time"
)

// Client is an HTTP client for a single horde node API endpoint.
type Client struct {
	baseURL    string
	http       *http.Client
	streamHTTP *http.Client
}

// httpTimeout is the default per-request timeout for client calls.
const httpTimeout = 10 * time.Second

// New constructs a client for the node at addr (host:port). A scheme prefix
// is optional; "http://" is assumed when absent.
func New(addr string) *Client {
	addr = strings.TrimSpace(addr)
	if !strings.HasPrefix(addr, "http://") && !strings.HasPrefix(addr, "https://") {
		addr = "http://" + addr
	}
	addr = strings.TrimRight(addr, "/")
	return &Client{
		baseURL:    addr,
		http:       &http.Client{Timeout: httpTimeout},
		streamHTTP: &http.Client{}, // no timeout: SSE streams live until closed
	}
}

// BaseURL returns the fully-qualified base URL the client targets
// (e.g. "http://localhost:13420"), for display in the TUI.
func (c *Client) BaseURL() string { return c.baseURL }

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

// SpawnAgent starts a new agent subprocess of the given name.
func (c *Client) SpawnAgent(ctx context.Context, name string) (Agent, error) {
	var a Agent
	body, _ := json.Marshal(map[string]string{"name": name})
	if err := c.postJSON(ctx, "/api/v1/agents", body, &a); err != nil {
		return a, err
	}
	return a, nil
}

// StopAgent stops an agent by id.
func (c *Client) StopAgent(ctx context.Context, id string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.baseURL+"/api/v1/agents/"+id, http.NoBody)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
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
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, http.NoBody)
	if err != nil {
		return nil, err
	}
	return c.http.Do(req)
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
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
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
