// Package cmd implements the horde CLI.
//
// The root command (`horde`) launches the TUI. Subcommands live in their
// own files: serve.go, agent.go, daemonize.go.
package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/geoffjay/horde/internal/config"
)

// version is injected at build time via -ldflags "-X cmd.version=...".
// It defaults to "dev" for `go build` without ldflags.
var version = "dev"

// rootCmd is the horde root command.
var rootCmd = &cobra.Command{
	Use:   "horde",
	Short: "Horde: a distributed multi-agent system built on the Google V2 ADK",
	Long: `horde is a collection of AI agents that can be executed and managed.

It can run in standalone mode (a single host as the central hub) or in a
multi-user distributed mode where one node is the master and others are
slaves. This relationship is largely invisible to the user on each system.

Run without a subcommand to launch the TUI.`,
	Version: version,
	RunE:    runTUI,
}

// Execute runs the root command.
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func init() {
	// Format `--version` output as "horde <version>" (single line) instead
	// of the verbose cobra default template.
	rootCmd.SetVersionTemplate("horde {{.Version}}\n")

	// Eagerly load configuration so subcommands have access to it via
	// config.Get(). Failures are deferred until a subcommand actually needs
	// the config; a missing file is not fatal because defaults cover the
	// base case.
	_ = config.Load()
}
