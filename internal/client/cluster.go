package client

import (
	"context"
	"net/url"
	"time"
)

// ClusterNode is one node in the cluster view returned by GET /cluster/nodes.
type ClusterNode struct {
	NodeID   string   `json:"node_id"`
	Addr     string   `json:"addr"`
	Agents   []string `json:"agents"`
	LastSeen string   `json:"last_seen"`
	Stale    bool     `json:"stale"`
}

// ClusterView is the GET /api/v1/cluster/nodes response: the leader's id
// plus every slave registered with this master.
type ClusterView struct {
	LeaderID string        `json:"leader_id"`
	Nodes    []ClusterNode `json:"nodes"`
}

// ListNodes fetches the cluster topology: the leader id and all registered
// slave nodes with their last-seen time, agent ids, and staleness.
func (c *Client) ListNodes(ctx context.Context) (ClusterView, error) {
	var v ClusterView
	if err := c.getJSON(ctx, "/api/v1/cluster/nodes", &v); err != nil {
		return v, err
	}
	return v, nil
}

// ListRemoteAgentContexts fetches the aggregated, redacted execution
// contexts from all slaves. Served by the master only. An optional issue
// filter narrows the results to contexts matching that issue.
func (c *Client) ListRemoteAgentContexts(ctx context.Context, issue string) ([]ExecutionContext, error) {
	path := "/api/v1/cluster/agents/context"
	if issue != "" {
		path += "?issue=" + url.QueryEscape(issue)
	}
	var ctxs []ExecutionContext
	if err := c.getJSON(ctx, path, &ctxs); err != nil {
		return nil, err
	}
	return ctxs, nil
}

// ParseLastSeen converts the RFC3339 last_seen string on a ClusterNode to a
// time.Time. It returns the zero time when the string is empty or unparseable.
func (n *ClusterNode) ParseLastSeen() time.Time {
	if n.LastSeen == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, n.LastSeen)
	if err != nil {
		return time.Time{}
	}
	return t
}
