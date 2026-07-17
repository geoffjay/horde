package server

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
)

// errStandaloneSlave is returned by newDiscoverer for a slave with the static
// mechanism and no leader configured. The caller treats it as "run without a
// leader connection" rather than a fatal error.
var errStandaloneSlave = errors.New("no leader configured; running standalone")

// Discoverer resolves the address of the leader (master) a slave connects to.
// It abstracts cluster.discovery_mechanism: "static" returns a configured
// address, "dns" looks up an SRV record so the leader can move, come up later,
// or be one of several targets without reconfiguring slaves. It is re-resolved
// on each reconnect/heartbeat, so a leader that changes address is picked up
// without a restart.
type Discoverer interface {
	// Leader returns the current leader address (host:port).
	Leader(ctx context.Context) (string, error)
	// Describe returns a short human-readable description for logs.
	Describe() string
}

// DiscoveryConfig selects and parameterizes leader discovery for a slave.
type DiscoveryConfig struct {
	// Mechanism is "static" (default) or "dns".
	Mechanism string
	// Leader is the configured leader host:port (static mechanism).
	Leader string
	// DNSName is the SRV name to look up (dns mechanism).
	DNSName string
}

const (
	discoveryStatic = "static"
	discoveryDNS    = "dns"
)

// newDiscoverer builds the Discoverer for a slave. It returns errStandaloneSlave
// for the static mechanism with no leader configured — the caller runs without
// a leader connection. It returns another error for an unknown mechanism or a
// dns mechanism missing its name.
func newDiscoverer(cfg DiscoveryConfig) (Discoverer, error) {
	switch cfg.Mechanism {
	case "", discoveryStatic:
		if cfg.Leader == "" {
			return nil, errStandaloneSlave
		}
		return &staticDiscoverer{addr: normalizeAddr(cfg.Leader)}, nil
	case discoveryDNS:
		if cfg.DNSName == "" {
			return nil, fmt.Errorf("discovery_mechanism %q requires cluster.discovery_dns_name", discoveryDNS)
		}
		return &dnsDiscoverer{name: cfg.DNSName, lookupSRV: net.DefaultResolver.LookupSRV}, nil
	default:
		return nil, fmt.Errorf("unknown cluster.discovery_mechanism %q (want %q or %q)", cfg.Mechanism, discoveryStatic, discoveryDNS)
	}
}

// normalizeAddr strips a scheme prefix from a configured address.
func normalizeAddr(addr string) string {
	addr = strings.TrimPrefix(addr, "http://")
	addr = strings.TrimPrefix(addr, "https://")
	return addr
}

// staticDiscoverer returns a fixed, configured leader address.
type staticDiscoverer struct{ addr string }

func (d *staticDiscoverer) Leader(context.Context) (string, error) {
	if d.addr == "" {
		return "", fmt.Errorf("no leader configured")
	}
	return d.addr, nil
}

func (d *staticDiscoverer) Describe() string { return "static(" + d.addr + ")" }

// dnsDiscoverer resolves the leader from an SRV record. lookupSRV is a seam for
// tests; it defaults to net.DefaultResolver.LookupSRV. Service and proto are
// passed empty so name is looked up as a full SRV name (e.g.
// "_horde._tcp.example.com").
type dnsDiscoverer struct {
	name      string
	lookupSRV func(ctx context.Context, service, proto, name string) (string, []*net.SRV, error)
}

func (d *dnsDiscoverer) Leader(ctx context.Context) (string, error) {
	_, addrs, err := d.lookupSRV(ctx, "", "", d.name)
	if err != nil {
		return "", fmt.Errorf("dns discovery: lookup SRV %q: %w", d.name, err)
	}
	if len(addrs) == 0 {
		return "", fmt.Errorf("dns discovery: no SRV records for %q", d.name)
	}
	best := pickSRV(addrs)
	host := strings.TrimSuffix(best.Target, ".")
	return net.JoinHostPort(host, strconv.Itoa(int(best.Port))), nil
}

func (d *dnsDiscoverer) Describe() string { return "dns(" + d.name + ")" }

// pickSRV chooses the preferred SRV target: lowest priority wins, ties broken
// by highest weight. (Weighted random selection within a priority is not
// needed — a slave just needs one reachable leader.)
func pickSRV(addrs []*net.SRV) *net.SRV {
	best := addrs[0]
	for _, a := range addrs[1:] {
		if a.Priority < best.Priority || (a.Priority == best.Priority && a.Weight > best.Weight) {
			best = a
		}
	}
	return best
}
