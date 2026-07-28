package cmd

import (
	"context"
	"fmt"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/geoffjay/horde/internal/app"
	"github.com/geoffjay/horde/internal/config"
)

// runTUI is the default action when `horde` is invoked with no subcommand.
// It launches the TUI as a pure client of the node API: it does not start a
// node. If no node is reachable at the configured host:port the TUI shows a
// 60-second retry countdown (with an immediate-retry key).
//
// The TUI does not log to stderr: it renders to the terminal, so stray log
// lines would corrupt the display. Instead it captures logs into an in-memory
// buffer surfaced on the logs page (app.Run wires logrus to it). When
// log.output is "file" the same lines are also teed to that file.
//
// --token / HORDE_USER_TOKEN sets the per-user bearer token sent on every
// request; required only when the target node has auth.users configured.
func runTUI(_ *cobra.Command, _ []string) error {
	cfg := config.Get()
	configureLogging(cfg)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	addr := fmt.Sprintf("localhost:%d", cfg.Server.Port)
	return app.Run(ctx, addr, tuiToken, logFileWriter(cfg))
}
