package server

import (
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSplitHostPortDefault(t *testing.T) {
	host, port, err := splitHostPortDefault("0.0.0.0:7946", defaultGossipPort)
	require.NoError(t, err)
	assert.Equal(t, "0.0.0.0", host)
	assert.Equal(t, 7946, port)

	// A bare host defaults the port.
	host, port, err = splitHostPortDefault("master", defaultGossipPort)
	require.NoError(t, err)
	assert.Equal(t, "master", host)
	assert.Equal(t, defaultGossipPort, port)

	// A non-numeric port is an error.
	_, _, err = splitHostPortDefault("host:abc", defaultGossipPort)
	assert.Error(t, err)
}

func TestResolveHostIP(t *testing.T) {
	// An IP passes through unchanged.
	ip, err := resolveHostIP("127.0.0.1")
	require.NoError(t, err)
	assert.Equal(t, "127.0.0.1", ip)

	// A hostname resolves to an address (localhost always resolves).
	ip, err = resolveHostIP("localhost")
	require.NoError(t, err)
	assert.NotEmpty(t, ip)
	assert.NotNil(t, net.ParseIP(ip), "resolved value is an IP")
}
