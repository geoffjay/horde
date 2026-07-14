package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"

	"github.com/geoffjay/horde/agents"
	"github.com/geoffjay/horde/internal/agentapi"
	"github.com/geoffjay/horde/internal/config"

	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/session"
)

// agentShutdownGrace is how long the agent HTTP server has to shut down
// gracefully before the process exits.
const agentShutdownGrace = 5 * time.Second

// agentReadHeaderTimeout is the ReadHeaderTimeout for the agent HTTP server.
// Prevents slowloris-style resource exhaustion (gosec G112).
const agentReadHeaderTimeout = 10 * time.Second

// agentName is the name of the agent a `horde agent` subprocess hosts.
var agentName string

// agentSocket is the unix domain socket path the agent subprocess listens on.
var agentSocket string

// agentWorkspace is the filesystem workspace path the agent operates within
// (advisory scope; passed from the project when the agent is spawned).
var agentWorkspace string

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
	agentCmd.Flags().StringVar(&agentSocket, "socket", "",
		"Unix domain socket path to listen on")
	agentCmd.Flags().StringVar(&agentWorkspace, "workspace", "",
		"Filesystem workspace path (advisory scope)")
	rootCmd.AddCommand(agentCmd)
}

// runAgent hosts a single ADK agent in this process, serving it over HTTP on
// a unix domain socket. The server spawns this subcommand as a subprocess;
// it blocks until the process is terminated.
func runAgent(_ *cobra.Command, _ []string) error {
	cfg := config.Get()
	setupLogging(cfg)

	logrus.WithField("agent", agentName).Info("agent subprocess starting")

	if agentWorkspace != "" {
		logrus.WithField("workspace", agentWorkspace).Debug("agent workspace scope")
	}

	// Resolve the socket path.
	if agentSocket == "" {
		agentSocket = filepath.Join(os.TempDir(),
			fmt.Sprintf("horde-agent-%d.sock", os.Getpid()))
	}

	// Look up the agent in the registry.
	a, err := agents.Get(agentName)
	if err != nil {
		return fmt.Errorf("lookup agent: %w", err)
	}

	// Build the runner.
	r, err := runner.New(runner.Config{
		AppName:           "horde",
		Agent:             a,
		SessionService:    session.InMemoryService(),
		AutoCreateSession: true,
	})
	if err != nil {
		return fmt.Errorf("create runner: %w", err)
	}

	// Remove any stale socket file before binding.
	_ = os.Remove(agentSocket)

	// Use net.ListenConfig so the listener respects a context for
	// cancellation during startup.
	lc := net.ListenConfig{}
	listener, err := lc.Listen(context.Background(), "unix", agentSocket)
	if err != nil {
		return fmt.Errorf("listen on socket %q: %w", agentSocket, err)
	}

	// Ensure the socket file is removed on exit.
	defer func() { _ = os.Remove(agentSocket) }()

	// Emit the ready handshake on stdout. The server reads this during
	// SpawnAgent to discover the socket path. Keep stdout clean after
	// this line — logrus writes to stderr.
	readyMsg, _ := json.Marshal(map[string]string{
		"type":   "spawn_ready",
		"socket": agentSocket,
	})
	fmt.Fprintln(os.Stdout, string(readyMsg))

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	h := agentapi.NewHandler(r, agentName)
	httpServer := &http.Server{
		Handler:           h.Router(),
		ReadHeaderTimeout: agentReadHeaderTimeout,
	}

	go func() {
		logrus.WithField("socket", agentSocket).Info("agent API listening")
		if err := httpServer.Serve(listener); err != nil && err != http.ErrServerClosed {
			logrus.WithError(err).Error("agent API listener failed")
		}
	}()

	<-ctx.Done()
	logrus.WithField("agent", agentName).Info("agent subprocess shutting down")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), agentShutdownGrace)
	defer cancel()
	_ = httpServer.Shutdown(shutdownCtx)
	return nil
}
