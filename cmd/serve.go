package cmd

import (
	"context"
	"fmt"
	"os/signal"
	"syscall"

	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"

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

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	srv, err := server.New(server.Config{
		Mode:              server.Mode(cfg.Mode),
		AgentCommand:      cfg.Server.AgentCommand,
		Leader:            cfg.Server.Leader,
		SpawnDefaultAgent: true,
	})
	if err != nil {
		return fmt.Errorf("create server: %w", err)
	}

	return srv.Run(ctx)
}
