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
)

// runTUI is the default action when `horde` is invoked with no subcommand.
// It launches the TUI as a pure client of the node API: it does not start a
// node. If no node is reachable at the configured host:port the TUI shows a
// 60-second retry countdown (with an immediate-retry key).
func runTUI(_ *cobra.Command, _ []string) error {
	cfg := config.Get()
	setupLogging(cfg)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	addr := fmt.Sprintf("localhost:%d", cfg.Server.Port)
	logrus.WithField("addr", addr).Info("launching horde TUI")
	return app.Run(ctx, addr)
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
