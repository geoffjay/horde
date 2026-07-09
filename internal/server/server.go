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
	"errors"
	"fmt"
	"net/http"
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
	// Port is the TCP port the node API listens on. Defaults to
	// defaultServerPort when zero.
	Port int
	// ReadTimeout, WriteTimeout, IdleTimeout are the API server timeouts in
	// seconds. Zero means use the stdlib default (no timeout).
	ReadTimeout  time.Duration
	WriteTimeout time.Duration
	IdleTimeout  time.Duration
	// NodeID is the unique identifier for this node within the cluster. When
	// empty a generated id is used. Populated from cluster.node_id.
	NodeID string
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
	slaves   map[string]knownSlave
	bus      *EventBus
	router   http.Handler
}

// AgentState is the lifecycle state of a spawned agent subprocess.
type AgentState string

const (
	// AgentRunning is the state of a healthy, running agent.
	AgentRunning AgentState = "running"
	// AgentExiting is the state of an agent that has been signaled to stop
	// but has not yet exited.
	AgentExiting AgentState = "exiting"
	// AgentExited is the state of an agent whose process has terminated.
	AgentExited AgentState = "exited"
)

// agentProc tracks one spawned agent subprocess.
type agentProc struct {
	id     string
	name   string
	state  AgentState
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
	// defaultServerPort is the default TCP port for the node API listener.
	defaultServerPort = 13420
)

// New constructs a Server for the given mode. Config is a value-type config
// bag passed once at construction; New copies the fields it needs into
// Server.cfg, so taking a pointer would force every caller to allocate a
// local for no real benefit.
func New(cfg Config) (*Server, error) { //nolint:gocritic // hugeParam
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
	if cfg.Port == 0 {
		cfg.Port = defaultServerPort
	}
	return &Server{
		cfg:    cfg,
		procs:  make(map[string]*agentProc),
		slaves: make(map[string]knownSlave),
		bus:    NewEventBus(),
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

// connectLeader attempts to reach the configured master node over the
// cluster API: it registers, then heartbeats on a ticker. It records
// connectivity status (leaderOK) without blocking local work. On failure
// it retries on the next tick.
func (s *Server) connectLeader(ctx context.Context) {
	if s.cfg.Leader == "" {
		logrus.Warn("slave mode without a configured leader; running standalone")
		return
	}

	client := newLeaderClient(s.cfg.Leader, s.cfg.NodeID, s.localAddr())

	// First registration: try immediately, then loop on the ticker.
	if leaderID, err := client.register(ctx); err != nil {
		logrus.WithError(err).WithField("leader", s.cfg.Leader).Warn("leader register failed")
	} else {
		s.mu.Lock()
		s.leaderOK = true
		s.mu.Unlock()
		logrus.WithFields(logrus.Fields{"leader": s.cfg.Leader, "leader_id": leaderID}).Info("registered with leader")
	}

	ticker := time.NewTicker(leaderReconnectInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if !s.leaderOK {
				if _, err := client.register(ctx); err != nil {
					logrus.WithError(err).WithField("leader", s.cfg.Leader).Debug("leader register retry failed")
					continue
				}
				s.mu.Lock()
				s.leaderOK = true
				s.mu.Unlock()
				logrus.WithField("leader", s.cfg.Leader).Info("registered with leader")
			}
			if err := client.heartbeat(ctx); err != nil {
				logrus.WithError(err).WithField("leader", s.cfg.Leader).Debug("heartbeat failed")
				s.mu.Lock()
				s.leaderOK = false
				s.mu.Unlock()
			}
		}
	}
}

// localAddr returns this slave's reachable address for the register
// payload. In this first version it derives from the configured leader
// plus the node's port; a real advertised address is a follow-up.
func (s *Server) localAddr() string {
	return fmt.Sprintf(":%d", s.cfg.Port)
}

// SpawnAgent starts a subprocess for the named agent and registers it. The
// name must correspond to an agent the binary knows how to host.
func (s *Server) SpawnAgent(ctx context.Context, name string) (string, error) {
	if _, err := agents.New(); err != nil {
		return "", fmt.Errorf("verify agent %q: %w", name, err)
	}

	s.mu.Lock()
	id := fmt.Sprintf("agent-%d-%d", s.nextID, time.Now().UnixNano())
	s.nextID++
	s.mu.Unlock()

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
		state:  AgentRunning,
		cmd:    cmd,
		doneCh: make(chan struct{}),
	}

	s.mu.Lock()
	s.procs[id] = proc
	s.mu.Unlock()

	logrus.WithFields(logrus.Fields{"agent": name, "id": id}).Info("agent started")

	go func() {
		_ = cmd.Wait()
		s.mu.Lock()
		if p, ok := s.procs[id]; ok {
			p.state = AgentExited
		}
		delete(s.procs, id)
		s.mu.Unlock()
		close(proc.doneCh)
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
		out = append(out, AgentInfo{ID: p.id, Name: p.name, Status: p.state})
	}
	return out
}

// AgentInfo describes a running agent.
type AgentInfo struct {
	ID     string
	Name   string
	Status AgentState
}

// StopAgent signals one agent by id to stop, mirroring Run's shutdown path:
// SIGTERM, then SIGKILL after agentShutdownGrace. It returns an error if the
// id is unknown or the agent has already exited.
func (s *Server) StopAgent(id string) error {
	s.mu.Lock()
	p, ok := s.procs[id]
	if !ok {
		s.mu.Unlock()
		return fmt.Errorf("agent %q not found", id)
	}
	p.state = AgentExiting
	s.mu.Unlock()

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
	return nil
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

// Port returns the TCP port the node API listens on.
func (s *Server) Port() int { return s.cfg.Port }

// NodeID returns the node's cluster identifier.
func (s *Server) NodeID() string { return s.cfg.NodeID }

// EventBus returns the node's in-process event bus, used by SSE handlers
// to stream agent invocation events to clients.
func (s *Server) EventBus() *EventBus { return s.bus }

// SetRouter injects the HTTP handler for the node API. When set before Run,
// Run starts an http.Server on the configured Port serving this handler.
// Injected by the caller (cmd) via api.Router to avoid an internal/api →
// internal/server import cycle.
func (s *Server) SetRouter(h http.Handler) { s.router = h }

// knownSlave tracks a slave that has registered with this master.
type knownSlave struct {
	addr string
}

// RegisterSlave records a slave's registration with this master. Only
// meaningful in master mode; in slave mode it is a no-op.
func (s *Server) RegisterSlave(nodeID, addr string) {
	if s.cfg.Mode != ModeMaster {
		return
	}
	s.mu.Lock()
	s.slaves[nodeID] = knownSlave{addr: addr}
	s.mu.Unlock()
	logrus.WithFields(logrus.Fields{"slave": nodeID, "addr": addr}).Debug("slave registered")
}

// Heartbeat records a heartbeat from a slave and returns the leader's node
// id and connectivity status. Only meaningful in master mode.
func (s *Server) Heartbeat(nodeID string) (string, bool) {
	if s.cfg.Mode != ModeMaster {
		return "", false
	}
	s.mu.Lock()
	if _, ok := s.slaves[nodeID]; !ok {
		s.slaves[nodeID] = knownSlave{}
	}
	s.mu.Unlock()
	return s.cfg.NodeID, true
}

// Run blocks until ctx is canceled, keeping the server alive. It is the
// main loop of `horde serve`.
//
// If a Router is set, Run starts an http.Server on the configured Port
// before blocking, and shuts it down on context cancel. The Router is
// injected (rather than Server importing internal/api) to keep the
// dependency direction clean: internal/api → internal/server, never the
// reverse.
func (s *Server) Run(ctx context.Context) error {
	if err := s.Start(ctx); err != nil {
		return err
	}

	var httpServer *http.Server
	// serveErr carries a fatal listener error (e.g. the port is already in
	// use) out of the background goroutine so Run can fail loudly instead of
	// staying up with a dead API. http.ErrServerClosed from Shutdown is not
	// sent here.
	serveErr := make(chan error, 1)
	if s.router != nil {
		addr := fmt.Sprintf(":%d", s.cfg.Port)
		httpServer = &http.Server{
			Addr:         addr,
			Handler:      s.router,
			ReadTimeout:  s.cfg.ReadTimeout,
			WriteTimeout: s.cfg.WriteTimeout,
			IdleTimeout:  s.cfg.IdleTimeout,
		}
		go func() {
			logrus.WithField("addr", addr).Info("node API listening")
			if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				logrus.WithError(err).Error("node API listener failed")
				serveErr <- err
			}
		}()
	}

	var runErr error
	select {
	case <-ctx.Done():
		runErr = ctx.Err()
	case err := <-serveErr:
		runErr = fmt.Errorf("node API listener: %w", err)
	}
	logrus.Info("horde node shutting down")

	if httpServer != nil {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), agentShutdownGrace)
		defer cancel()
		_ = httpServer.Shutdown(shutdownCtx)
	}

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
	return runErr
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
