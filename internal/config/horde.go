// Package config also defines the horde application configuration that
// extends the generic config with horde-specific sections (env, mode,
// server, leader). This mirrors the pattern used by plantd/identity.
package config

import (
	"encoding/base64"
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
	// DiscoveryMechanism is how a slave finds its leader: "static" (via
	// server.leader), "dns" (an SRV lookup of DiscoveryDNSName), or "gossip"
	// (a peer-to-peer membership ring; the master advertises itself).
	DiscoveryMechanism string `mapstructure:"discovery_mechanism"`
	// DiscoveryDNSName is the SRV name a slave looks up when DiscoveryMechanism
	// is "dns" (e.g. "_horde._tcp.example.com"). The lowest-priority target's
	// host:port becomes the leader address.
	DiscoveryDNSName string `mapstructure:"discovery_dns_name"`
	// AdvertiseAddr is the reachable host:port this node advertises to peers
	// (sent to the master on register so it can route back to this node).
	// Empty falls back to ":<port>", which is not routable across hosts.
	AdvertiseAddr string `mapstructure:"advertise_addr"`
	// GossipBindAddr / GossipAdvertiseAddr are the host:port the gossip
	// listeners bind and advertise when DiscoveryMechanism is "gossip". Empty
	// uses the memberlist LAN defaults (0.0.0.0:7946).
	GossipBindAddr      string `mapstructure:"gossip_bind_addr"`
	GossipAdvertiseAddr string `mapstructure:"gossip_advertise_addr"`
	// GossipSeeds is a comma-separated list of gossip addresses a node joins to
	// bootstrap ring membership (e.g. "master:7946"). A scalar (not a list) so
	// it also works via HORDE_CLUSTER_GOSSIP_SEEDS. A slave needs at least one.
	GossipSeeds string `mapstructure:"gossip_seeds"`
	// AuthToken is a shared secret required on node→node cluster calls
	// (register/heartbeat/events). When set, outbound cluster calls send it as
	// a bearer token and the master rejects unauthenticated ones. Empty
	// disables cluster request auth (backward compatible).
	AuthToken string `mapstructure:"auth_token"`
	// GossipEncryptionKey is a base64-encoded 16/24/32-byte key that encrypts
	// gossip traffic (memberlist SecretKey, AES-128/192/256). All nodes must
	// share it. Empty leaves gossip unencrypted.
	GossipEncryptionKey string `mapstructure:"gossip_encryption_key"`
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

// ProjectConfig represents project-related configuration.
type ProjectConfig struct {
	// WorkspaceDir is the default workspace directory for a project whose
	// create request omits the workspace path. Defaults to the current
	// directory.
	WorkspaceDir string `mapstructure:"workspace_dir"`
	// ContextRetention is how long a finished project's agent contexts are
	// retained before eviction, in seconds. Zero inherits the agent
	// context_retention value.
	ContextRetention int `mapstructure:"context_retention"`
}

// AgentKind is the kind of an agent definition: a registry-built native ADK
// agent or an external AAP adapter driven over the stdio binding.
type AgentKind string

const (
	// AgentKindADK is a native ADK agent built in-process on
	// google.golang.org/adk/v2 and hosted by the `horde agent` subprocess.
	AgentKindADK AgentKind = "adk"
	// AgentKindAAP is an external agent driven through an AAP v1 adapter
	// subprocess over the stdio binding.
	AgentKindAAP AgentKind = "aap"
)

// PermissionScope is the advisory filesystem permission scope sent to an AAP
// adapter in initialize.permissions (capability permissions). A compliant
// adapter self-enforces it independent of tool approval.
type PermissionScope struct {
	// Mode is the access mode: "read_only" or "read_write".
	Mode string `mapstructure:"mode"`
	// WritablePaths narrows writes when Mode is "read_write". Paths are
	// relative to the workspace cwd unless absolute. Empty means the whole
	// workspace is writable.
	WritablePaths []string `mapstructure:"writable_paths"`
	// DenyPaths are paths the adapter must not read or write, overriding the
	// rest.
	DenyPaths []string `mapstructure:"deny_paths"`
}

// AgentDef declares a named agent. Native ADK agents (greeter, repeater) are
// looked up in the agents registry by name; an AgentDef with Kind "aap"
// instead configures an external AAP adapter subprocess, so an operator can
// add an external agent without recompiling.
type AgentDef struct {
	// Kind is the agent kind. Defaults to "adk" when empty (handled at
	// resolution time, not by viper, so an unset kind is a native ADK agent).
	Kind AgentKind `mapstructure:"kind"`
	// Command is the AAP adapter command (argv[0]). AAP only.
	Command string `mapstructure:"command"`
	// Args are the adapter argv after the command. AAP only.
	Args []string `mapstructure:"args"`
	// Env is extra environment for the adapter subprocess, merged over the
	// node's environment. The host sets AAP_TRANSPORT=stdio regardless. Keys
	// preserve case (env vars are case-sensitive). AAP only.
	Env []EnvPair `mapstructure:"env"`
	// Model is the requested model, passed as initialize.model. Empty uses
	// the adapter default. AAP only.
	Model string `mapstructure:"model"`
	// SystemPrompt is a system prompt text or path for initialize.system_prompt.
	// AAP only.
	SystemPrompt string `mapstructure:"system_prompt"`
	// SystemPromptMode is "replace" (default) or "append" (requires the
	// system_prompt_append capability). AAP only.
	SystemPromptMode string `mapstructure:"system_prompt_mode"`
	// Permissions is the advisory filesystem scope sent in
	// initialize.permissions. Empty omits the scope. AAP only.
	Permissions *PermissionScope `mapstructure:"permissions"`
	// AutoApprove, when true and the adapter advertises tool_approval,
	// auto-allows every approval_request. When false (the default) a request
	// stays pending until a decision endpoint (follow-up) resolves it or the
	// turn ends. AAP only.
	AutoApprove bool `mapstructure:"auto_approve"`
	// MCPServers provisions MCP servers for the adapter (initialize.tools),
	// keyed by server name (requires the mcp capability). AAP only.
	MCPServers map[string]MCPServerDef `mapstructure:"mcp_servers"`
}

// EnvPair is one environment variable for an AAP adapter subprocess. The
// slice form (rather than a map) preserves key case: viper lowercases map
// keys, but environment variable names are case-sensitive.
type EnvPair struct {
	Key   string `mapstructure:"key"`
	Value string `mapstructure:"value"`
}

// MCPServerDef is a stdio MCP server the host provisions for an AAP adapter via
// initialize.tools.mcp_servers. Env uses the EnvPair slice form for the same
// case-preservation reason as the adapter env.
type MCPServerDef struct {
	Command string    `mapstructure:"command"`
	Args    []string  `mapstructure:"args"`
	Env     []EnvPair `mapstructure:"env"`
}

// DataPaths holds the XDG-compliant on-disk directories horde uses for
// configuration, general storage, and trivial state. Each is overridable via
// its respective env var; see the persistence decision doc.
type DataPaths struct {
	// ConfigDir is the configuration directory (~/.config/horde).
	ConfigDir string `mapstructure:"config_dir"`
	// DataDir is the general storage directory (~/.local/share/horde):
	// logs, auth, session data, database files.
	DataDir string `mapstructure:"data_dir"`
	// StateDir is the trivial state directory (~/.local/state/horde):
	// JSON KV, execution state, agent info, prompt history, lock files.
	StateDir string `mapstructure:"state_dir"`
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
	Project ProjectConfig `mapstructure:"project"`
	// Agents declares named agents. Native ADK agents (greeter, repeater) are
	// registry-built and need no entry here; an entry with Kind "aap"
	// configures an external AAP adapter. The map is keyed by agent name.
	Agents  map[string]AgentDef `mapstructure:"agents"`
	Log     LogConfig           `mapstructure:"log"`
	Service ServiceConfig       `mapstructure:"service"`
	Paths   DataPaths           `mapstructure:"paths"`
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

	defaultProjectWorkspaceDir = "."

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
	"cluster.node_id":               "",
	"cluster.discovery_mechanism":   "static",
	"cluster.discovery_dns_name":    "",
	"cluster.gossip_bind_addr":      "",
	"cluster.gossip_advertise_addr": "",
	"cluster.gossip_seeds":          "",
	"cluster.advertise_addr":        "",
	"cluster.auth_token":            "",
	"cluster.gossip_encryption_key": "",

	// Agent defaults
	"agent.socket_dir":           "/tmp",
	"agent.ready_timeout":        defaultAgentReadyTimeout,
	"agent.health_poll_interval": defaultAgentHealthPollInterval,
	"agent.context_retention":    defaultAgentContextRetention,
	"agent.context_share":        defaultAgentContextShare,

	// Project defaults
	"project.workspace_dir":     defaultProjectWorkspaceDir,
	"project.context_retention": 0, // 0 inherits agent.context_retention

	// Data paths (XDG); empty means resolve from home dir at load time.
	"paths.config_dir": "",
	"paths.data_dir":   "",
	"paths.state_dir":  "",

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
	c.resolvePaths()
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

	switch c.Cluster.DiscoveryMechanism {
	case "", "static":
	case "dns":
		if c.Cluster.DiscoveryDNSName == "" {
			return fmt.Errorf("cluster.discovery_mechanism \"dns\" requires cluster.discovery_dns_name")
		}
	case "gossip":
		// A slave must know at least one seed to join the ring; a master is
		// typically the seed itself, so seeds are optional for it.
		if c.Mode == "slave" && c.Cluster.GossipSeeds == "" {
			return fmt.Errorf("cluster.discovery_mechanism \"gossip\" requires cluster.gossip_seeds on a slave")
		}
	default:
		return fmt.Errorf("invalid cluster.discovery_mechanism %q: want static, dns, or gossip", c.Cluster.DiscoveryMechanism)
	}

	if c.Cluster.GossipEncryptionKey != "" {
		key, err := base64.StdEncoding.DecodeString(c.Cluster.GossipEncryptionKey)
		if err != nil {
			return fmt.Errorf("cluster.gossip_encryption_key must be base64: %w", err)
		}
		switch len(key) {
		case 16, 24, 32: //nolint:mnd // AES-128/192/256 key sizes
		default:
			return fmt.Errorf("cluster.gossip_encryption_key must decode to 16, 24, or 32 bytes, got %d", len(key))
		}
	}

	return nil
}
