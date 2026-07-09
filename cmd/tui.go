package cmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"

	"github.com/geoffjay/horde/internal/app"
	"github.com/geoffjay/horde/internal/config"
	"github.com/geoffjay/horde/internal/server"
)

// runTUI is the default action when `horde` is invoked with no subcommand.
func runTUI(_ *cobra.Command, _ []string) error {
	cfg := config.Get()
	setupLogging(cfg)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	srv, err := server.New(server.Config{
		Mode:              server.Mode(cfg.Mode),
		SpawnDefaultAgent: true,
	})
	if err != nil {
		return fmt.Errorf("create server: %w", err)
	}

	// Allow the TUI to stop the server it manages.
	tuiCtx := app.WithCancel(ctx, func() {
		stop()
	})

	// Start the server in-process so the TUI can interact with it.
	if err := srv.Start(tuiCtx); err != nil {
		return fmt.Errorf("start server: %w", err)
	}

	logrus.Info("launching horde TUI")
	return app.Run(tuiCtx, srv)
}

// setupLogging configures the global logrus logger from the app config.
func setupLogging(cfg *config.Config) {
	switch cfg.Log.Formatter {
	case "json":
		logrus.SetFormatter(&logrus.JSONFormatter{})
	default:
		logrus.SetFormatter(&logrus.TextFormatter{FullTimestamp: true})
	}

	level, err := logrus.ParseLevel(cfg.Log.Level)
	if err != nil {
		level = logrus.InfoLevel
	}
	logrus.SetLevel(level)
	logrus.SetOutput(os.Stderr)
}
