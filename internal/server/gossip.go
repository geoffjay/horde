package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/hashicorp/memberlist"
	"github.com/sirupsen/logrus"
)

const (
	// roleMaster / roleSlave are the values carried in nodeMeta.Role.
	roleMaster = "master"
	roleSlave  = "slave"

	// defaultGossipPort is the memberlist bind/advertise port used when a
	// gossip address omits one.
	defaultGossipPort = 7946

	// gossipLeaveTimeout bounds the graceful Leave broadcast on shutdown.
	gossipLeaveTimeout = 5 * time.Second
	// gossipRejoinInterval is how often a node with seeds retries Join while
	// no master is visible in the ring.
	gossipRejoinInterval = 5 * time.Second
)

// nodeMeta is the per-node metadata gossiped in the memberlist NodeMeta field.
// It is intentionally tiny — memberlist caps NodeMeta at 512 bytes — and
// carries no sensitive data (a role and the node's HTTP address).
type nodeMeta struct {
	Role    string `json:"role"`
	APIAddr string `json:"api_addr"`
}

// gossipConfig parameterizes a gossip node.
type gossipConfig struct {
	NodeID        string
	Role          string
	APIAddr       string   // this node's HTTP address, advertised to peers
	BindAddr      string   // host:port to bind the gossip listeners; empty → LAN default
	AdvertiseAddr string   // host:port peers use to reach this node; empty → derived
	Seeds         []string // gossip addresses to Join (host or host:port)
}

// gossipNode wraps a memberlist so the cluster can discover the leader's HTTP
// address peer-to-peer. Every node (master and slave) runs one; the master
// advertises Role=master so slaves can find it via the gossiped membership.
type gossipNode struct {
	ml    *memberlist.Memberlist
	seeds []string

	done chan struct{}
	once sync.Once
}

// gossipDelegate supplies this node's metadata to memberlist. It carries no
// user messages — only NodeMeta — so the message hooks are no-ops.
type gossipDelegate struct{ meta []byte }

func (d *gossipDelegate) NodeMeta(limit int) []byte {
	if len(d.meta) > limit {
		return d.meta[:limit]
	}
	return d.meta
}

func (d *gossipDelegate) NotifyMsg([]byte)                {}
func (d *gossipDelegate) GetBroadcasts(_, _ int) [][]byte { return nil }
func (d *gossipDelegate) LocalState(bool) []byte          { return nil }
func (d *gossipDelegate) MergeRemoteState([]byte, bool)   {}

// newGossipNode creates and starts a gossip node. Creating the memberlist
// binds the gossip listeners (fatal on failure). If seeds are configured it
// attempts an initial Join (non-fatal) and starts a background loop that
// retries Join while no master is visible, so a node that starts before the
// master converges without a restart.
//
//nolint:gocritic // hugeParam: gossipConfig is a one-shot construction argument
func newGossipNode(cfg gossipConfig) (*gossipNode, error) {
	meta, err := json.Marshal(nodeMeta{Role: cfg.Role, APIAddr: cfg.APIAddr})
	if err != nil {
		return nil, fmt.Errorf("gossip: encode meta: %w", err)
	}

	mlCfg := memberlist.DefaultLANConfig()
	mlCfg.Name = cfg.NodeID
	mlCfg.Delegate = &gossipDelegate{meta: meta}
	// memberlist logs verbosely to stderr by default; silence it and rely on
	// this package's own join/leader logging.
	mlCfg.LogOutput = io.Discard

	if cfg.BindAddr != "" {
		host, port, err := splitHostPortDefault(cfg.BindAddr, defaultGossipPort)
		if err != nil {
			return nil, fmt.Errorf("gossip: bind addr %q: %w", cfg.BindAddr, err)
		}
		mlCfg.BindAddr = host
		mlCfg.BindPort = port
	}
	if cfg.AdvertiseAddr != "" {
		host, port, err := splitHostPortDefault(cfg.AdvertiseAddr, defaultGossipPort)
		if err != nil {
			return nil, fmt.Errorf("gossip: advertise addr %q: %w", cfg.AdvertiseAddr, err)
		}
		// memberlist requires the advertise address to be an IP; resolve a
		// hostname (natural in docker/k8s) to one.
		ip, err := resolveHostIP(host)
		if err != nil {
			return nil, fmt.Errorf("gossip: resolve advertise host %q: %w", host, err)
		}
		mlCfg.AdvertiseAddr = ip
		mlCfg.AdvertisePort = port
	}

	ml, err := memberlist.Create(mlCfg)
	if err != nil {
		return nil, fmt.Errorf("gossip: create memberlist: %w", err)
	}

	n := &gossipNode{ml: ml, seeds: cfg.Seeds, done: make(chan struct{})}
	if len(cfg.Seeds) > 0 {
		if _, err := ml.Join(cfg.Seeds); err != nil {
			logrus.WithError(err).WithField("seeds", cfg.Seeds).Warn("gossip: initial join failed; retrying in background")
		}
		go n.rejoinLoop()
	}
	return n, nil
}

// rejoinLoop retries Join while no master is visible in the ring, until the
// node shuts down. Once a master is present it idles (a cheap membership check
// each interval), so a master that comes up late — or restarts — is picked up
// without a restart of this node.
func (n *gossipNode) rejoinLoop() {
	ticker := time.NewTicker(gossipRejoinInterval)
	defer ticker.Stop()
	for {
		if _, err := n.leaderAPIAddr(); err != nil {
			if _, jerr := n.ml.Join(n.seeds); jerr != nil {
				logrus.WithError(jerr).Debug("gossip: rejoin attempt failed")
			}
		}
		select {
		case <-n.done:
			return
		case <-ticker.C:
		}
	}
}

// leaderAPIAddr scans the gossiped membership for the master and returns its
// advertised HTTP address. It errors when no master is visible yet (the caller
// — the leaderClient — retries on its next reconnect tick).
func (n *gossipNode) leaderAPIAddr() (string, error) {
	for _, m := range n.ml.Members() {
		var meta nodeMeta
		if err := json.Unmarshal(m.Meta, &meta); err != nil {
			continue
		}
		if meta.Role == roleMaster && meta.APIAddr != "" {
			return meta.APIAddr, nil
		}
	}
	return "", fmt.Errorf("gossip: no master visible in the cluster yet (%d members)", n.ml.NumMembers())
}

// describe returns a short human-readable description for logs.
func (n *gossipNode) describe() string {
	return fmt.Sprintf("gossip(%d members)", n.ml.NumMembers())
}

// shutdown leaves the ring gracefully (broadcasting departure) and tears down
// the listeners. It is safe to call once; the ctx-cancel goroutine in Start
// invokes it on shutdown.
func (n *gossipNode) shutdown() {
	n.once.Do(func() { close(n.done) })
	if err := n.ml.Leave(gossipLeaveTimeout); err != nil {
		logrus.WithError(err).Debug("gossip: leave failed")
	}
	if err := n.ml.Shutdown(); err != nil {
		logrus.WithError(err).Debug("gossip: shutdown failed")
	}
}

// resolveHostIP returns host unchanged when it is already an IP, else resolves
// it to one address (preferring IPv4). memberlist's advertise address must be
// an IP, but operators naturally configure hostnames (docker/k8s service names).
func resolveHostIP(host string) (string, error) {
	if net.ParseIP(host) != nil {
		return host, nil
	}
	addrs, err := net.DefaultResolver.LookupIPAddr(context.Background(), host)
	if err != nil {
		return "", err
	}
	for _, a := range addrs {
		if v4 := a.IP.To4(); v4 != nil {
			return v4.String(), nil
		}
	}
	if len(addrs) > 0 {
		return addrs[0].IP.String(), nil
	}
	return "", fmt.Errorf("no addresses for %q", host)
}

// splitHostPortDefault splits a "host:port" address, defaulting the port when
// it is omitted ("host" alone). Other malformed addresses return an error.
//
//nolint:gocritic // unnamedResult: host, port, err are clear from context
func splitHostPortDefault(addr string, defPort int) (string, int, error) {
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		var addrErr *net.AddrError
		if errors.As(err, &addrErr) && addrErr.Err == "missing port in address" {
			return addr, defPort, nil
		}
		return "", 0, err
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return "", 0, fmt.Errorf("invalid port %q: %w", portStr, err)
	}
	return host, port, nil
}
