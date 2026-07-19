package server

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/raft"
	raftboltdb "github.com/hashicorp/raft-boltdb/v2"
	"github.com/sirupsen/logrus"
)

const (
	// defaultRaftPort is the raft transport port used when a raft address omits
	// one. It sits just above the default API port to keep a node's ports
	// clustered.
	defaultRaftPort = 13421
	// raftMaxPool is the number of connections the raft transport pools per peer.
	raftMaxPool = 3
	// raftTransportTimeout bounds a single raft transport round trip.
	raftTransportTimeout = 10 * time.Second
	// raftApplyTimeout bounds a raft.Apply / membership change.
	raftApplyTimeout = 10 * time.Second
	// raftSnapshotRetain is how many raft snapshots to keep on disk.
	raftSnapshotRetain = 2
)

// errNoRaftLeader is returned by the raftDiscoverer while no leader is elected.
var errNoRaftLeader = errors.New("raft: no leader elected yet")

// raftFSMHandler is implemented by the Server to apply replicated commands and
// snapshot/restore the master-only state. It is injected into the FSM so the
// raft package stays free of project/resume specifics. Nil during slice 1
// (election only) — the FSM then no-ops, which is safe because no commands are
// applied until the state stores are wired through raft.
type raftFSMHandler interface {
	// applyCommand applies one replicated command (the bytes passed to
	// raft.Apply) to the local state machine.
	applyCommand(data []byte) error
	// snapshotState returns a point-in-time serialization of the replicated
	// state for a raft snapshot.
	snapshotState() ([]byte, error)
	// restoreState replaces the replicated state from a snapshot.
	restoreState(data []byte) error
}

// raftConfig parameterizes a raftNode.
type raftConfig struct {
	NodeID        string
	BindAddr      string // host:port to bind the raft transport; empty → 0.0.0.0:<advertise port|default>
	AdvertiseAddr string // host:port peers dial to reach this node's raft transport
	DataDir       string // directory for the raft log, stable store, and snapshots
	Bootstrap     bool   // bootstrap a new single-node cluster when no state exists (the --mode master hint)
	Handler       raftFSMHandler
}

// raftNode wraps a hashicorp/raft instance: the leader of the raft cluster is
// the horde master. Every failover node runs one; membership is reconciled from
// the gossip ring (the leader AddVoter/RemoveServers peers as they join/leave).
type raftNode struct {
	raft      *raft.Raft
	transport *raft.NetworkTransport
	store     *raftboltdb.BoltStore
	localAddr string
}

// newRaftNode builds and starts a raft node. It binds the transport (fatal on
// failure), opens the bolt log/stable store and file snapshot store under
// DataDir, and, when Bootstrap is set and no prior state exists, bootstraps a
// single-node cluster with itself as the only voter (peers are added later by
// the leader's membership reconcile loop).
//
//nolint:gocritic // hugeParam: raftConfig is a one-shot construction argument
func newRaftNode(cfg raftConfig) (*raftNode, error) {
	if cfg.AdvertiseAddr == "" {
		return nil, errors.New("raft: advertise address is required")
	}
	if err := os.MkdirAll(cfg.DataDir, stateDirPerm); err != nil {
		return nil, fmt.Errorf("raft: create data dir: %w", err)
	}

	advTCP, err := net.ResolveTCPAddr("tcp", cfg.AdvertiseAddr)
	if err != nil {
		return nil, fmt.Errorf("raft: resolve advertise addr %q: %w", cfg.AdvertiseAddr, err)
	}

	bindAddr := cfg.BindAddr
	if bindAddr == "" {
		_, port, perr := splitHostPortDefault(cfg.AdvertiseAddr, defaultRaftPort)
		if perr != nil {
			return nil, fmt.Errorf("raft: advertise addr %q: %w", cfg.AdvertiseAddr, perr)
		}
		bindAddr = fmt.Sprintf("0.0.0.0:%d", port)
	}

	transport, err := raft.NewTCPTransport(bindAddr, advTCP, raftMaxPool, raftTransportTimeout, io.Discard)
	if err != nil {
		return nil, fmt.Errorf("raft: create transport: %w", err)
	}

	store, err := raftboltdb.New(raftboltdb.Options{Path: filepath.Join(cfg.DataDir, "raft.db")})
	if err != nil {
		_ = transport.Close()
		return nil, fmt.Errorf("raft: open bolt store: %w", err)
	}

	snapshots, err := raft.NewFileSnapshotStore(cfg.DataDir, raftSnapshotRetain, io.Discard)
	if err != nil {
		_ = store.Close()
		_ = transport.Close()
		return nil, fmt.Errorf("raft: create snapshot store: %w", err)
	}

	rc := raft.DefaultConfig()
	rc.LocalID = raft.ServerID(cfg.NodeID)
	rc.Logger = hclog.NewNullLogger()

	r, err := raft.NewRaft(rc, &raftFSM{handler: cfg.Handler}, store, store, snapshots, transport)
	if err != nil {
		_ = store.Close()
		_ = transport.Close()
		return nil, fmt.Errorf("raft: create raft: %w", err)
	}

	if cfg.Bootstrap {
		if err := bootstrapIfNeeded(r, store, snapshots, rc.LocalID, transport.LocalAddr()); err != nil {
			return nil, err
		}
	}

	return &raftNode{
		raft:      r,
		transport: transport,
		store:     store,
		localAddr: string(transport.LocalAddr()),
	}, nil
}

// bootstrapIfNeeded bootstraps a single-node cluster (this node as the only
// voter) when no prior raft state exists; peers are added later by the leader's
// membership reconcile. It is a no-op when state already exists (a restart).
func bootstrapIfNeeded(r *raft.Raft, store *raftboltdb.BoltStore, snaps raft.SnapshotStore, id raft.ServerID, addr raft.ServerAddress) error {
	hasState, err := raft.HasExistingState(store, store, snaps)
	if err != nil {
		return fmt.Errorf("raft: check existing state: %w", err)
	}
	if hasState {
		return nil
	}
	bcfg := raft.Configuration{Servers: []raft.Server{{ID: id, Address: addr}}}
	if err := r.BootstrapCluster(bcfg).Error(); err != nil {
		return fmt.Errorf("raft: bootstrap: %w", err)
	}
	return nil
}

// isLeader reports whether this node currently holds raft leadership.
func (n *raftNode) isLeader() bool { return n.raft.State() == raft.Leader }

// leaderID returns the node id of the current raft leader, or "" if none is
// elected yet.
func (n *raftNode) leaderID() string {
	_, id := n.raft.LeaderWithID()
	return string(id)
}

// servers returns the current raft configuration as nodeID→address. It is only
// meaningful on the leader (a follower may return a stale set), which is where
// membership reconcile runs.
func (n *raftNode) servers() (map[string]string, error) {
	f := n.raft.GetConfiguration()
	if err := f.Error(); err != nil {
		return nil, err
	}
	out := make(map[string]string)
	for _, srv := range f.Configuration().Servers {
		out[string(srv.ID)] = string(srv.Address)
	}
	return out, nil
}

// addVoter adds a peer as a voting member. Leader-only; a no-op error on a
// follower.
func (n *raftNode) addVoter(nodeID, addr string) error {
	return n.raft.AddVoter(raft.ServerID(nodeID), raft.ServerAddress(addr), 0, raftApplyTimeout).Error()
}

// removeServer removes a peer from the configuration. Leader-only.
func (n *raftNode) removeServer(nodeID string) error {
	return n.raft.RemoveServer(raft.ServerID(nodeID), 0, raftApplyTimeout).Error()
}

// shutdown stops raft and closes the transport and store. Safe to call once on
// ctx cancel (mirrors gossipNode.shutdown).
func (n *raftNode) shutdown() {
	if err := n.raft.Shutdown().Error(); err != nil {
		logrus.WithError(err).Debug("raft: shutdown failed")
	}
	if err := n.transport.Close(); err != nil {
		logrus.WithError(err).Debug("raft: transport close failed")
	}
	if err := n.store.Close(); err != nil {
		logrus.WithError(err).Debug("raft: store close failed")
	}
}
