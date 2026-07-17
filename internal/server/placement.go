package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Placement decides which node a spawn request runs on. The master is the
// cluster entry point: it can spawn locally or forward the spawn to a
// registered slave, whose local agents endpoint spawns the subprocess and
// heartbeats it back so cross-node invoke routing then reaches it.

// nodeLocal is the reserved placement target meaning "this node".
const nodeLocal = "local"

// nodeAuto is the placement target that asks the master to choose the
// least-loaded node (itself or a non-stale slave).
const nodeAuto = "auto"

// slaveSpawnTimeout bounds a master→slave spawn forward. It is longer than the
// heartbeat round-trip (leaderClientTimeout) because a spawn runs the agent's
// ready handshake (ADK) or the AAP initialize→ready handshake on the slave.
const slaveSpawnTimeout = 30 * time.Second

// ErrNodeNotFound is returned when a placement targets a node that is not a
// known, non-stale slave (and is not the local node).
var ErrNodeNotFound = errors.New("placement node not found or not reachable")

// ErrPlacementMasterOnly is returned when a non-master node is asked to place
// an agent on a different node. Direction is master→slave only.
var ErrPlacementMasterOnly = errors.New("remote agent placement is only available on the master node")

// ResolveSpawnTarget maps a requested placement node to a concrete target.
// requested may be:
//   - "" / "local" / this node's id → spawn on this node (local=true).
//   - "auto" → the least-loaded node among {this node, non-stale slaves}.
//   - a slave node id → that slave, if it is registered and not stale.
//
// It returns (addr, local, err): local=true means spawn here (addr empty);
// otherwise addr is the reachable slave address to forward the spawn to.
//
//nolint:gocritic // unnamedResult: result meanings are documented above
func (s *Server) ResolveSpawnTarget(requested string) (string, bool, error) {
	if requested == "" || requested == nodeLocal || requested == s.cfg.NodeID {
		return "", true, nil
	}

	// Remote placement is a master capability: only the master holds the slave
	// registry and the aggregated view needed to route.
	if s.cfg.Mode != ModeMaster {
		return "", false, ErrPlacementMasterOnly
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	s.evictStaleSlavesLocked(now)

	if requested == nodeAuto {
		addr, local := s.leastLoadedTargetLocked(now)
		return addr, local, nil
	}

	sl, ok := s.slaves[requested]
	if !ok || sl.addr == "" || now.Sub(sl.lastSeen) > slaveStaleAfter {
		return "", false, fmt.Errorf("%w: %q", ErrNodeNotFound, requested)
	}
	return sl.addr, false, nil
}

// leastLoadedTargetLocked picks the node with the fewest agents among this node
// and all non-stale slaves, breaking ties in favor of the local node (no
// network hop). The caller must hold s.mu. Load is agent count: local uses the
// live proc map, slaves use their last-reported agent list. Returns (addr,
// local): local=true means spawn here (addr empty).
func (s *Server) leastLoadedTargetLocked(now time.Time) (string, bool) {
	bestLocal := true
	bestAddr := ""
	bestLoad := len(s.procs)

	for _, sl := range s.slaves {
		if sl.addr == "" || now.Sub(sl.lastSeen) > slaveStaleAfter {
			continue
		}
		if load := len(sl.agents); load < bestLoad {
			bestLoad = load
			bestAddr = sl.addr
			bestLocal = false
		}
	}
	return bestAddr, bestLocal
}

// ForwardSpawn posts a spawn request to a slave's agents endpoint and returns
// its HTTP status, headers, and body verbatim so the caller can relay the
// slave's response (including the id it assigned). The forwarded body carries
// only the agent name — never a node — so the slave spawns locally and the
// request cannot loop. Master-only in practice (callers reach it via
// ResolveSpawnTarget).
//
//nolint:gocritic // unnamedResult: mirrors ForwardProjectRequest's signature
func (s *Server) ForwardSpawn(ctx context.Context, addr, name string) (int, http.Header, []byte, error) {
	body, err := json.Marshal(createAgentPayload{Name: name})
	if err != nil {
		return 0, nil, nil, err
	}

	url := fmt.Sprintf("http://%s/api/v1/agents", addr)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return 0, nil, nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: slaveSpawnTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, nil, fmt.Errorf("forward spawn to %s: %w", addr, err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, nil, nil, fmt.Errorf("read spawn response: %w", err)
	}
	return resp.StatusCode, resp.Header, respBody, nil
}

// createAgentPayload mirrors the createAgentRequest shape in internal/api. The
// forwarded spawn sends only the name so the receiving slave spawns locally.
type createAgentPayload struct {
	Name string `json:"name"`
}
