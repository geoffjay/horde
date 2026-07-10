package cmd

import (
	"context"
	"fmt"
	"os/signal"
	"syscall"

	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"

	"github.com/geoffjay/horde/agents"
	"github.com/geoffjay/horde/internal/config"
)

// agentName is the name of the agent a `horde agent` subprocess hosts.
var agentName string

// agentCmd is a hidden subcommand the server spawns as a subprocess to host
// a single ADK agent.
var agentCmd = &cobra.Command{
	Use:    "agent",
	Short:  "Host an ADK agent as a subprocess (invoked by the server)",
	Hidden: true,
	RunE:   runAgent,
}

func init() {
	agentCmd.Flags().StringVar(&agentName, "name", "greeter",
		"Name of the agent to host")
	rootCmd.AddCommand(agentCmd)
}

// runAgent hosts a single ADK agent in this process. The server spawns this
// subcommand as a subprocess; it blocks running the agent until the process
// is terminated.
func runAgent(_ *cobra.Command, _ []string) error {
	cfg := config.Get()
	setupLogging(cfg)

	logrus.WithField("agent", agentName).Info("agent subprocess starting")

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	a, err := agents.New()
	if err != nil {
		return fmt.Errorf("create agent: %w", err)
	}

	// For this first version the agent host simply blocks until asked to
	// stop; the agent is constructed to validate wiring. Real invocation
	// will be driven by the server API in the next phase.
	_ = a
	<-ctx.Done()
	// A signal-driven shutdown (SIGINT/SIGTERM) is a normal exit, not an
	// error; returning context.Canceled makes cobra print "Error: ...".
	return nil
}
