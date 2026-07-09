// Package config also defines the horde application configuration that
// extends the generic config with horde-specific sections (env, mode,
// server, leader). This mirrors the pattern used by plantd/identity.
package config

import (
	"sync"

	log "github.com/sirupsen/logrus"
)

// ServerConfig represents the horde node server configuration.
type ServerConfig struct {
	// Port the node listens on for its API.
	Port int `mapstructure:"port"`
	// AgentCommand is the path to the binary used to host agent subprocesses.
	// When empty, the server uses the current executable (`horde agent`).
	AgentCommand string `mapstructure:"agent_command"`
	// Leader is the address of the master node that a slave connects to. Only
	// used when Mode == "slave".
	Leader string `mapstructure:"leader"`
	// ReadTimeout, WriteTimeout, IdleTimeout are the API server timeouts in
	// seconds.
	ReadTimeout  int `mapstructure:"read_timeout"`
	WriteTimeout int `mapstructure:"write_timeout"`
	IdleTimeout  int `mapstructure:"idle_timeout"`
}

// ClusterConfig represents the distributed cluster configuration.
type ClusterConfig struct {
	// NodeID is the unique identifier for this node within the cluster. When
	// empty a generated id is used.
	NodeID string `mapstructure:"node_id"`
	// DiscoveryMechanism is how nodes find each other. Initially "static"
	// (configured via Leader), future options: dns, gossip.
	DiscoveryMechanism string `mapstructure:"discovery_mechanism"`
}

// Config represents the configuration for the horde application.
//
// It embeds the generic config pieces (Log, Service) and adds horde-specific
// sections. This follows the same extension pattern as plantd/identity.
type Config struct {
	Env     string        `mapstructure:"env"`
	Mode    string        `mapstructure:"mode"`
	Server  ServerConfig  `mapstructure:"server"`
	Cluster ClusterConfig `mapstructure:"cluster"`
	Log     LogConfig     `mapstructure:"log"`
	Service ServiceConfig `mapstructure:"service"`
}

var (
	lock    = &sync.Mutex{}
	current *Config
)

// Default configuration values.
const (
	defaultServerPort         = 13420
	defaultServerReadTimeout  = 30
	defaultServerWriteTimeout = 30
	defaultServerIdleTimeout  = 120
)

// defaults defines the default configuration values for horde.
var defaults = map[string]any{
	"env":  "development",
	"mode": "master",

	// Server defaults
	"server.port":          defaultServerPort,
	"server.agent_command": "",
	"server.leader":        "",
	"server.read_timeout":  defaultServerReadTimeout,
	"server.write_timeout": defaultServerWriteTimeout,
	"server.idle_timeout":  defaultServerIdleTimeout,

	// Cluster defaults
	"cluster.node_id":             "",
	"cluster.discovery_mechanism": "static",

	// Logging defaults
	"log.formatter": "text",
	"log.level":     "info",

	// Service defaults
	"service.id": "org.horde.Horde",
}

// Load loads the horde configuration into the package singleton. It is safe
// to call multiple times; only the first call performs the load.
func Load() error {
	lock.Lock()
	defer lock.Unlock()
	if current != nil {
		return nil
	}

	c := &Config{}
	if err := LoadConfigWithDefaults("horde", c, defaults); err != nil {
		return err
	}
	current = c
	return nil
}

// Get returns the application configuration singleton, loading it on first
// use if Load was not called explicitly.
func Get() *Config {
	if current != nil {
		return current
	}
	lock.Lock()
	if current != nil {
		lock.Unlock()
		return current
	}
	c := &Config{}
	if err := LoadConfigWithDefaults("horde", c, defaults); err != nil {
		lock.Unlock()
		log.Fatalf("error reading config file: %s\n", err)
	}
	current = c
	lock.Unlock()
	return current
}

// Reset clears the cached configuration. Intended for tests.
func Reset() {
	lock.Lock()
	defer lock.Unlock()
	current = nil
}

// Validate validates the configuration settings.
func (c *Config) Validate() error {
	// TODO: validate mode is master|slave, port ranges, leader set in slave.
	return nil
}
