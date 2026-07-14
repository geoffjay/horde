// Package config also defines the horde application configuration that
// extends the generic config with horde-specific sections (env, mode,
// server, leader). This mirrors the pattern used by plantd/identity.
package config

import (
	"fmt"
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

// AgentConfig represents agent subprocess configuration.
type AgentConfig struct {
	// SocketDir is the directory for agent unix socket files.
	SocketDir string `mapstructure:"socket_dir"`
	// ReadyTimeout is how long to wait for an agent subprocess ready
	// handshake, in seconds.
	ReadyTimeout int `mapstructure:"ready_timeout"`
	// HealthPollInterval is how often to poll each agent's /health, in
	// seconds. Zero disables polling.
	HealthPollInterval int `mapstructure:"health_poll_interval"`
	// ContextRetention is how long an agent's execution context is retained
	// after the agent exits, in seconds.
	ContextRetention int `mapstructure:"context_retention"`
	// ContextShare controls remote-visible scope: "restricted" (redacted
	// subset) or "full".
	ContextShare string `mapstructure:"context_share"`
}

// AdapterConfig configures an external AAP agent adapter that the node can
// drive over the stdio binding (see the `horde aap-run` command). It is
// distinct from AgentConfig, which governs the node's own ADK agent
// subprocesses.
type AdapterConfig struct {
	// Command is the adapter executable (argv[0]).
	Command string `mapstructure:"command"`
	// Args are the arguments passed to Command.
	Args []string `mapstructure:"args"`
	// Env are extra environment variables set for the adapter process, in
	// addition to the node's environment and the AAP transport variables.
	Env map[string]string `mapstructure:"env"`
	// Model is the AAP model string sent in initialize. Empty lets the adapter
	// (and its agent) choose its configured default.
	Model string `mapstructure:"model"`
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
	Agent   AgentConfig   `mapstructure:"agent"`
	// Adapters are external AAP agent adapters, keyed by name, that the node
	// can drive over the stdio binding (see `horde aap-run --agent <name>`).
	Adapters map[string]AdapterConfig `mapstructure:"adapters"`
	Log      LogConfig                `mapstructure:"log"`
	Service  ServiceConfig            `mapstructure:"service"`
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

	defaultAgentReadyTimeout       = 5
	defaultAgentHealthPollInterval = 30
	defaultAgentContextRetention   = 300
	defaultAgentContextShare       = "restricted"

	// maxPort is the largest valid TCP port number.
	maxPort = 65535
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

	// Agent defaults
	"agent.socket_dir":           "/tmp",
	"agent.ready_timeout":        defaultAgentReadyTimeout,
	"agent.health_poll_interval": defaultAgentHealthPollInterval,
	"agent.context_retention":    defaultAgentContextRetention,
	"agent.context_share":        defaultAgentContextShare,

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
	return loadLocked()
}

// loadLocked loads, validates, and caches the configuration. The caller must
// hold lock.
func loadLocked() error {
	c := &Config{}
	if err := LoadConfigWithDefaults("horde", c, defaults); err != nil {
		return err
	}
	if err := c.Validate(); err != nil {
		return fmt.Errorf("invalid configuration: %w", err)
	}
	current = c
	return nil
}

// Get returns the application configuration singleton, loading it on first
// use if Load was not called explicitly.
func Get() *Config {
	lock.Lock()
	if current == nil {
		if err := loadLocked(); err != nil {
			lock.Unlock()
			log.Fatalf("error reading config file: %s\n", err)
		}
	}
	c := current
	lock.Unlock()
	return c
}

// Reset clears the cached configuration. Intended for tests.
func Reset() {
	lock.Lock()
	defer lock.Unlock()
	current = nil
}

// Validate validates the configuration settings. It rejects an unknown node
// mode, an out-of-range server port, and negative timeouts.
//
// A slave without a configured leader is intentionally allowed: the server
// treats that as a standalone slave (see server.connectLeader), so it is a
// warning at runtime rather than a validation error here.
func (c *Config) Validate() error {
	switch c.Mode {
	case "master", "slave":
	default:
		return fmt.Errorf("invalid mode %q: want master or slave", c.Mode)
	}

	if c.Server.Port < 1 || c.Server.Port > maxPort {
		return fmt.Errorf("server.port %d out of range 1-%d", c.Server.Port, maxPort)
	}

	for name, v := range map[string]int{
		"server.read_timeout":  c.Server.ReadTimeout,
		"server.write_timeout": c.Server.WriteTimeout,
		"server.idle_timeout":  c.Server.IdleTimeout,
	} {
		if v < 0 {
			return fmt.Errorf("%s must not be negative, got %d", name, v)
		}
	}

	return nil
}
