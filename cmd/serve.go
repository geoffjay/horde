package cmd

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"

	"github.com/geoffjay/horde/internal/api"
	"github.com/geoffjay/horde/internal/config"
	"github.com/geoffjay/horde/internal/server"
)

// serveMode is the node role selected via `--mode`.
var serveMode string

// serveDaemonize detaches the server into the background when set.
var serveDaemonize bool

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Start the horde node",
	Long: `Start the horde node in the foreground.

By default the node runs in master (leader) mode. Pass --mode slave to start
as a slave that connects to a leader; local functionality is not blocked while
the leader connection is being established.`,
	RunE: runServe,
}

func init() {
	serveCmd.Flags().StringVar(&serveMode, "mode", "master",
		"Node mode: master (leader, default) or slave")
	serveCmd.Flags().BoolVar(&serveDaemonize, "daemonize", false,
		"Detach and run the server in the background")
	rootCmd.AddCommand(serveCmd)
}

// runServe starts the horde node.
func runServe(cmd *cobra.Command, _ []string) error {
	cfg := config.Get()
	// The flag takes precedence over config when explicitly set; otherwise
	// the config value (which defaults to master) is used.
	if cmd.Flags().Changed("mode") {
		cfg.Mode = serveMode
	}
	setupLogging(cfg)

	if serveDaemonize {
		return daemonize()
	}

	logrus.WithField("mode", cfg.Mode).Info("starting horde node")

	if err := cfg.EnsureDataDirs(); err != nil {
		return fmt.Errorf("ensure data dirs: %w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	gossipKey, err := decodeGossipKey(cfg.Cluster.GossipEncryptionKey)
	if err != nil {
		return fmt.Errorf("cluster.gossip_encryption_key: %w", err)
	}

	srv, err := server.New(server.Config{
		Mode:                server.Mode(cfg.Mode),
		AgentCommand:        cfg.Server.AgentCommand,
		Leader:              cfg.Server.Leader,
		DiscoveryMechanism:  cfg.Cluster.DiscoveryMechanism,
		DiscoveryDNSName:    cfg.Cluster.DiscoveryDNSName,
		GossipBindAddr:      cfg.Cluster.GossipBindAddr,
		GossipAdvertiseAddr: cfg.Cluster.GossipAdvertiseAddr,
		GossipSeeds:         splitSeeds(cfg.Cluster.GossipSeeds),
		AuthToken:           cfg.Cluster.AuthToken,
		GossipEncryptionKey: gossipKey,
		Failover:            cfg.Cluster.Failover,
		RaftBindAddr:        cfg.Cluster.RaftBindAddr,
		RaftAdvertiseAddr:   cfg.Cluster.RaftAdvertiseAddr,
		RaftDir:             cfg.Cluster.RaftDir,
		SpawnDefaultAgent:   true,
		Port:                cfg.Server.Port,
		ReadTimeout:         time.Duration(cfg.Server.ReadTimeout) * time.Second,
		WriteTimeout:        time.Duration(cfg.Server.WriteTimeout) * time.Second,
		IdleTimeout:         time.Duration(cfg.Server.IdleTimeout) * time.Second,
		NodeID:              cfg.Cluster.NodeID,
		AdvertiseAddr:       cfg.Cluster.AdvertiseAddr,
		SocketDir:           cfg.Agent.SocketDir,
		ReadyTimeout:        time.Duration(cfg.Agent.ReadyTimeout) * time.Second,
		HealthPollInterval:  time.Duration(cfg.Agent.HealthPollInterval) * time.Second,
		ContextRetention:    time.Duration(cfg.Agent.ContextRetention) * time.Second,
		ContextShareFull:    cfg.Agent.ContextShare == "full",
		DataDir:             cfg.Paths.DataDir,
		StateDir:            cfg.Paths.StateDir,
		ProjectWorkspaceDir: cfg.Project.WorkspaceDir,
		AgentDefs:           buildServerAgentDefs(cfg.Agents),
	})
	if err != nil {
		return fmt.Errorf("create server: %w", err)
	}

	srv.SetRouter(api.Router(srv))

	if err := srv.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}

// buildServerAgentDefs maps the config-layer agent declarations into the
// server's value-type AgentDef. Only AAP entries are forwarded (an ADK entry
// is redundant — the registry is the source of truth for native agents — so
// it is skipped to avoid shadowing a registry agent with an empty def).
func buildServerAgentDefs(cfgs map[string]config.AgentDef) map[string]server.AgentDef {
	out := make(map[string]server.AgentDef, len(cfgs))
	for name := range cfgs {
		c := cfgs[name]
		if c.Kind != config.AgentKindAAP {
			continue
		}
		def := server.AgentDef{
			Kind:             server.AgentKindAAP,
			Command:          c.Command,
			Args:             c.Args,
			Model:            c.Model,
			SystemPrompt:     c.SystemPrompt,
			SystemPromptMode: c.SystemPromptMode,
			AutoApprove:      c.AutoApprove,
		}
		for _, p := range c.Env {
			def.Env = append(def.Env, server.EnvPair{Key: p.Key, Value: p.Value})
		}
		if len(c.MCPServers) > 0 {
			def.MCPServers = make(map[string]server.MCPServerDef, len(c.MCPServers))
			for name := range c.MCPServers {
				m := c.MCPServers[name]
				sm := server.MCPServerDef{Command: m.Command, Args: m.Args}
				for _, p := range m.Env {
					sm.Env = append(sm.Env, server.EnvPair{Key: p.Key, Value: p.Value})
				}
				def.MCPServers[name] = sm
			}
		}
		if c.Permissions != nil {
			def.Permissions = &server.PermissionScope{
				Mode:          c.Permissions.Mode,
				WritablePaths: c.Permissions.WritablePaths,
				DenyPaths:     c.Permissions.DenyPaths,
			}
		}
		out[name] = def
	}
	return out
}

// decodeGossipKey base64-decodes the gossip encryption key into the raw bytes
// memberlist expects (16/24/32 for AES-128/192/256). Empty yields a nil key
// (gossip unencrypted). Config validation already checks the length.
func decodeGossipKey(key string) ([]byte, error) {
	if key == "" {
		return nil, nil
	}
	return base64.StdEncoding.DecodeString(key)
}

// splitSeeds parses the comma-separated cluster.gossip_seeds value into a slice
// of gossip addresses, trimming whitespace and dropping empties.
func splitSeeds(seeds string) []string {
	if seeds == "" {
		return nil
	}
	var out []string
	for _, s := range strings.Split(seeds, ",") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}
