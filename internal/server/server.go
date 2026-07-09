// Package server implements the horde node: a long-running process that
// spawns and manages subprocess agents built on the V2 ADK from Google.
//
// A node runs in one of two modes:
//
//   - master (leader): the central hub. Local agents are managed directly
//     and the node is the source of truth for the cluster.
//   - slave: connects to a master node, but is not blocked by that
//     connection for local functionality. Local agents run immediately; the
//     leader connection is established in the background.
//
// The server exposes an API for communication with clients (the TUI and any
// other consumers). The exact API transport is intentionally left as a stub
// for a later phase; for now Server.Run blocks until ctx is canceled and
// keeps the registered agent subprocesses alive.
package server

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/geoffjay/horde/agents"
)

// Mode is the node role.
type Mode string

const (
	// ModeMaster is the leader role.
	ModeMaster Mode = "master"
	// ModeSlave is the follower role.
	ModeSlave Mode = "slave"
)

// Config configures a Server.
type Config struct {
	// Mode is the node role: master (default) or slave.
	Mode Mode
	// AgentCommand is the path to the agent binary that the server spawns as
	// a subprocess for each registered agent. If empty it defaults to the
	// current executable invoked with the "agent" subcommand, which is how
	// the horde binary serves as its own agent host.
	AgentCommand string
	// Leader is the address of the master node. Only used in slave mode.
	Leader string
	// SpawnDefaultAgent controls whether Start spawns the default greeter
	// agent. Tests set this to false to avoid spawning real subprocesses.
	SpawnDefaultAgent bool
}

// Server is the horde node. It owns a set of agent subprocesses and, when
// Run is called, blocks until the supplied context is canceled. In slave
// mode it additionally attempts to connect to a leader in the background.
type Server struct {
	cfg Config

	mu       sync.Mutex
	procs    map[string]*agentProc
	nextID   int
	running  bool
	leaderOK bool
}

// agentProc tracks one spawned agent subprocess.
type agentProc struct {
	id     string
	name   string
	cmd    *exec.Cmd
	doneCh chan struct{}
}

const (
	// leaderReconnectInterval is how often a slave retries the leader
	// connection (background, never blocks local work).
	leaderReconnectInterval = 5 * time.Second
	// agentShutdownGrace is how long we wait for an agent subprocess to exit
	// after signaling it before force-killing.
	agentShutdownGrace = 5 * time.Second
)

// New constructs a Server for the given mode.
func New(cfg Config) (*Server, error) {
	if cfg.Mode == "" {
		cfg.Mode = ModeMaster
	}
	switch cfg.Mode {
	case ModeMaster, ModeSlave:
	default:
		return nil, fmt.Errorf("invalid mode %q: want master or slave", cfg.Mode)
	}
	if cfg.AgentCommand == "" {
		cfg.AgentCommand = defaultAgentCommand()
	}
	return &Server{
		cfg:   cfg,
		procs: make(map[string]*agentProc),
	}, nil
}

// Start prepares the server and spawns the initial set of agents. It returns
// quickly; Run is what blocks.
func (s *Server) Start(ctx context.Context) error {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return fmt.Errorf("server already running")
	}
	s.running = true
	s.mu.Unlock()

	logrus.WithField("mode", s.cfg.Mode).Info("horde node starting")

	// Spawn the default agent. In this first version there is exactly one:
	// the hello-world greeter. This happens in both modes — local
	// functionality is never blocked by the leader connection. Tests can
	// disable this via SpawnDefaultAgent.
	if s.cfg.SpawnDefaultAgent {
		if _, err := s.SpawnAgent(ctx, "greeter"); err != nil {
			return fmt.Errorf("spawn default agent: %w", err)
		}
	}

	// In slave mode, establish the leader connection in the background so it
	// never blocks local operation.
	if s.cfg.Mode == ModeSlave {
		go s.connectLeader(ctx)
	}

	return nil
}

// connectLeader attempts to reach the configured master node. This is a
// placeholder for the real cluster transport (next phase): it retries in the
// background and records connectivity status without blocking local work.
func (s *Server) connectLeader(ctx context.Context) {
	if s.cfg.Leader == "" {
		logrus.Warn("slave mode without a configured leader; running standalone")
		return
	}

	ticker := time.NewTicker(leaderReconnectInterval)
	defer ticker.Stop()

	for {
		// TODO: replace with a real health/registration RPC against the
		// leader. For now we just mark connectivity as attempted.
		logrus.WithField("leader", s.cfg.Leader).Debug("attempting leader connection")
		s.mu.Lock()
		s.leaderOK = true
		s.mu.Unlock()

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// SpawnAgent starts a subprocess for the named agent and registers it. The
// name must correspond to an agent the binary knows how to host.
func (s *Server) SpawnAgent(ctx context.Context, name string) (string, error) {
	if _, err := agents.New(); err != nil {
		return "", fmt.Errorf("verify agent %q: %w", name, err)
	}

	id := fmt.Sprintf("agent-%d-%d", s.nextID, time.Now().UnixNano())
	s.nextID++

	cmdCtx, cancel := context.WithCancel(context.Background())
	args := []string{"agent", "--name", name}
	// AgentCommand is operator-controlled config, not untrusted user input.
	cmd := exec.CommandContext(cmdCtx, s.cfg.AgentCommand, args...) //#nosec G204
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Cancel = func() error {
		// Send SIGTERM for a graceful shutdown, fall back to SIGKILL after
		// a grace period handled by exec.CommandContext.
		_ = cmd.Process.Signal(os.Interrupt)
		return nil
	}

	if err := cmd.Start(); err != nil {
		cancel()
		return "", fmt.Errorf("start agent %q: %w", name, err)
	}

	proc := &agentProc{
		id:     id,
		name:   name,
		cmd:    cmd,
		doneCh: make(chan struct{}),
	}

	s.mu.Lock()
	s.procs[id] = proc
	s.mu.Unlock()

	logrus.WithFields(logrus.Fields{"agent": name, "id": id}).Info("agent started")

	go func() {
		_ = cmd.Wait()
		close(proc.doneCh)
		s.mu.Lock()
		delete(s.procs, id)
		s.mu.Unlock()
		logrus.WithField("id", id).Info("agent exited")
	}()

	// Wire cancellation to the caller's ctx so stopping the server tears
	// down spawned agents.
	go func() {
		select {
		case <-ctx.Done():
			cancel()
		case <-proc.doneCh:
			cancel()
		}
	}()

	return id, nil
}

// Agents returns a snapshot of currently running agent subprocesses.
func (s *Server) Agents() []AgentInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]AgentInfo, 0, len(s.procs))
	for _, p := range s.procs {
		out = append(out, AgentInfo{ID: p.id, Name: p.name})
	}
	return out
}

// AgentInfo describes a running agent.
type AgentInfo struct {
	ID   string
	Name string
}

// LeaderConnected reports whether the slave's leader connection is currently
// established. Always true for master mode.
func (s *Server) LeaderConnected() bool {
	if s.cfg.Mode == ModeMaster {
		return true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.leaderOK
}

// Mode returns the node's configured role.
func (s *Server) Mode() Mode { return s.cfg.Mode }

// Run blocks until ctx is canceled, keeping the server alive. It is the
// main loop of `horde serve`.
func (s *Server) Run(ctx context.Context) error {
	if err := s.Start(ctx); err != nil {
		return err
	}

	<-ctx.Done()
	logrus.Info("horde node shutting down")

	// Tear down any remaining agent subprocesses.
	s.mu.Lock()
	procs := make([]*agentProc, 0, len(s.procs))
	for _, p := range s.procs {
		procs = append(procs, p)
	}
	s.mu.Unlock()

	for _, p := range procs {
		if p.cmd.Process != nil {
			_ = p.cmd.Process.Signal(os.Interrupt)
		}
		select {
		case <-p.doneCh:
		case <-time.After(agentShutdownGrace):
			if p.cmd.Process != nil {
				_ = p.cmd.Process.Kill()
			}
		}
	}
	return ctx.Err()
}

// defaultAgentCommand returns the path of the current executable, so the
// horde binary can host its own agents as subprocesses of itself.
func defaultAgentCommand() string {
	exe, err := os.Executable()
	if err != nil {
		return "horde"
	}
	return exe
}
