package server

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewDiscoverer(t *testing.T) {
	// static with a leader → static discoverer, scheme stripped.
	d, err := newDiscoverer(DiscoveryConfig{Mechanism: "static", Leader: "http://master:13420"})
	require.NoError(t, err)
	require.IsType(t, &staticDiscoverer{}, d)
	addr, err := d.Leader(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "master:13420", addr)

	// empty mechanism defaults to static.
	d, err = newDiscoverer(DiscoveryConfig{Leader: "master:13420"})
	require.NoError(t, err)
	assert.IsType(t, &staticDiscoverer{}, d)

	// static with no leader → standalone sentinel.
	d, err = newDiscoverer(DiscoveryConfig{Mechanism: "static"})
	assert.ErrorIs(t, err, errStandaloneSlave, "a slave with no leader is standalone")
	assert.Nil(t, d)

	// dns with a name → dns discoverer.
	d, err = newDiscoverer(DiscoveryConfig{Mechanism: "dns", DNSName: "_horde._tcp.example.com"})
	require.NoError(t, err)
	assert.IsType(t, &dnsDiscoverer{}, d)

	// dns without a name → error.
	_, err = newDiscoverer(DiscoveryConfig{Mechanism: "dns"})
	require.Error(t, err)

	// unknown mechanism → error.
	_, err = newDiscoverer(DiscoveryConfig{Mechanism: "gossip"})
	require.Error(t, err)
}

func TestDNSDiscoverer_PicksPreferredTarget(t *testing.T) {
	d := &dnsDiscoverer{
		name: "_horde._tcp.example.com",
		lookupSRV: func(_ context.Context, _, _, _ string) (string, []*net.SRV, error) {
			return "", []*net.SRV{
				{Target: "backup.example.com.", Port: 13420, Priority: 20, Weight: 100},
				{Target: "leader.example.com.", Port: 13420, Priority: 10, Weight: 5},
				{Target: "leader-hi-weight.example.com.", Port: 13420, Priority: 10, Weight: 50},
			}, nil
		},
	}
	addr, err := d.Leader(context.Background())
	require.NoError(t, err)
	// Lowest priority (10) wins; within it the highest weight (50); trailing
	// dot trimmed; host:port joined.
	assert.Equal(t, "leader-hi-weight.example.com:13420", addr)
}

func TestDNSDiscoverer_Errors(t *testing.T) {
	lookErr := &dnsDiscoverer{
		name: "x",
		lookupSRV: func(_ context.Context, _, _, _ string) (string, []*net.SRV, error) {
			return "", nil, &net.DNSError{Err: "no such host", Name: "x"}
		},
	}
	_, err := lookErr.Leader(context.Background())
	assert.Error(t, err)

	empty := &dnsDiscoverer{
		name: "x",
		lookupSRV: func(_ context.Context, _, _, _ string) (string, []*net.SRV, error) {
			return "", nil, nil
		},
	}
	_, err = empty.Leader(context.Background())
	assert.Error(t, err, "no SRV records is an error")
}

// TestLeaderClient_ResolvesViaDNSThenRegisters wires a leader client with a dns
// discoverer (injected resolver) to a bare register endpoint, exercising the
// resolve→register path and confirming leaderAddr() reflects the resolved
// address (empty until the first resolve, since dns does not seed the cache).
func TestLeaderClient_ResolvesViaDNSThenRegisters(t *testing.T) {
	var gotPath string
	master := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true,"node_id":"slave-1","leader_id":"master-1"}`)
	}))
	defer master.Close()

	host, portStr, err := net.SplitHostPort(master.Listener.Addr().String())
	require.NoError(t, err)
	port, err := strconv.Atoi(portStr)
	require.NoError(t, err)
	srvPort := uint16(port) //#nosec G115 -- a real listener's port always fits uint16

	disco := &dnsDiscoverer{
		name: "_horde._tcp.test",
		lookupSRV: func(_ context.Context, _, _, _ string) (string, []*net.SRV, error) {
			return "", []*net.SRV{{Target: host + ".", Port: srvPort}}, nil
		},
	}
	c := newLeaderClient(disco, "slave-1", "slave1:13420")
	assert.Empty(t, c.leaderAddr(), "dns discoverer does not seed the cached address")

	leaderID, err := c.register(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "master-1", leaderID)
	assert.Equal(t, "/api/v1/cluster/register", gotPath)
	assert.Equal(t, net.JoinHostPort(host, portStr), c.leaderAddr(), "leaderAddr reflects the resolved address after register")
}

func TestLeaderClient_StaticSeedsCachedAddr(t *testing.T) {
	disco, err := newDiscoverer(DiscoveryConfig{Leader: "master:13420"})
	require.NoError(t, err)
	c := newLeaderClient(disco, "slave-1", "")
	assert.Equal(t, "master:13420", c.leaderAddr(), "static discoverer seeds leaderAddr before the first register")
}
