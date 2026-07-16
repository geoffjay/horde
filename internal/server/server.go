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
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/geoffjay/horde/agents"
	"github.com/geoffjay/horde/internal/aap"
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
	// SocketDir is the directory for agent unix socket files. Defaults to
	// os.TempDir when empty.
	SocketDir string
	// ReadyTimeout is how long to wait for an agent subprocess ready
	// handshake. Defaults to defaultReadyTimeout when zero.
	ReadyTimeout time.Duration
	// HealthPollInterval is how often to poll each agent's /health. Defaults
	// to defaultHealthPollInterval when zero. Zero disables polling.
	HealthPollInterval time.Duration
	// ContextRetention is how long an agent's execution context is retained
	// after the agent exits before it is evicted. Zero disables auto-eviction
	// (the entry is kept until the process ends).
	ContextRetention time.Duration
	// ContextShareFull, when true, exposes full (un-redacted) execution
	// context to remote principals on this node's own endpoints. When false
	// (the default), remote principals get the redacted subset + counts.
	ContextShareFull bool
	// DataDir is the general storage directory for logs, auth, session data,
	// and database files. May be empty (persistence not yet wired).
	DataDir string
	// StateDir is the trivial state directory for JSON KV, execution state,
	// agent info, prompt history, and lock files. May be empty.
	StateDir string
	// ProjectWorkspaceDir is the default workspace directory for a project
	// whose create request omits the workspace path.
	ProjectWorkspaceDir string
	// AgentDefs declares named agents, keyed by name. An entry with Kind
	// "aap" configures an external AAP adapter; "adk" (or absent) falls back
	// to the agents registry. Populated from config by cmd/serve.go.
	AgentDefs map[string]AgentDef
}

// AgentKind is the kind of a spawned agent: a native ADK agent or an external
// AAP adapter.
type AgentKind string

const (
	// AgentKindADK is a native ADK agent hosted by the `horde agent` subprocess.
	AgentKindADK AgentKind = "adk"
	// AgentKindAAP is an external agent driven through an AAP v1 adapter
	// subprocess over the stdio binding.
	AgentKindAAP AgentKind = "aap"
)

// AgentDef declares a named agent for the server. Native ADK agents are
// registry-built; an AAP def configures an external adapter subprocess.
type AgentDef struct {
	Kind         AgentKind
	Command      string
	Args         []string
	Env          []EnvPair
	Model        string
	SystemPrompt string
	// SystemPromptMode is "replace" (default) or "append".
	SystemPromptMode string
	Permissions      *PermissionScope
	AutoApprove      bool
	// MCPServers are MCP server definitions provisioned via
	// initialize.tools.mcp_servers, keyed by server name. AAP only.
	MCPServers map[string]MCPServerDef
}

// MCPServerDef is a stdio MCP server definition sent in initialize.tools.
type MCPServerDef struct {
	Command string
	Args    []string
	Env     []EnvPair
}

// EnvPair is one environment variable for an AAP adapter subprocess.
type EnvPair struct {
	Key   string
	Value string
}

// PermissionScope is the advisory filesystem scope sent to an AAP adapter in
// initialize.permissions.
type PermissionScope struct {
	Mode          string
	WritablePaths []string
	DenyPaths     []string
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
	ctxStore *contextStore
	projects ProjectStore

	// resume persists the latest AAP resume_token per agent so a respawned
	// adapter can resume its prior conversation.
	resume *resumeStore

	// remoteContexts holds contexts reported by slaves, keyed by
	// (nodeID, agentID). Only populated on a master.
	remoteContexts map[string]ExecutionContext

	// leader is the HTTP client to the master node, set in slave mode when
	// a leader is configured. API handlers use it to forward project reads
	// and mutations to the master so project state is cluster-wide.
	leader *leaderClient

	// aapInvokes tracks active and recently-finished AAP invocations per
	// agent, keyed by invocation id, so a reconnecting client with
	// Last-Event-ID can resume from the per-invocation ring buffer. Only
	// AAP agents use this; ADK agents keep their buffer in the agentapi
	// subprocess.
	aapInvokes *aapInvocationRegistry

	// now returns the current time. A field so tests can inject a clock when
	// exercising slave staleness; defaults to time.Now.
	now func() time.Time
}

// ErrAgentNotFound is returned by StopAgent when the given id is unknown.
// Callers (e.g. the API's DELETE handler) match it with errors.Is rather
// than string-comparing the error message.
var ErrAgentNotFound = errors.New("agent not found")

// ErrNotAAPAgent is returned when an operation valid only for AAP agents
// (e.g. resolving a tool approval) is attempted on a native ADK agent.
var ErrNotAAPAgent = errors.New("agent is not an AAP agent")

// ErrApprovalNotFound is returned when a tool-approval request id is not
// pending: unknown, already resolved, or cleared when the turn ended.
var ErrApprovalNotFound = errors.New("approval request not found")

// logKeyAgent is the logrus field key for an agent name.
const logKeyAgent = "agent"

// logKeyProject is the logrus field key for a project id.
const logKeyProject = "project"

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
	id            string
	name          string
	kind          AgentKind
	state         AgentState
	cmd           *exec.Cmd
	doneCh        chan struct{}
	socketPath    string          // populated from the subprocess ready handshake (ADK only)
	healthy       bool            // true unless a health poll has failed
	activeProject string          // active project id; empty when no project assigned
	aapSession    *aapHostSession // AAP only; nil for ADK agents
}

const (
	// leaderReconnectInterval is how often a slave retries the leader
	// connection (background, never blocks local work).
	leaderReconnectInterval = 5 * time.Second
	// agentShutdownGrace is how long we wait for an agent subprocess to exit
	// after signaling it before force-killing.
	agentShutdownGrace = 5 * time.Second
	// slaveStaleAfter is how long since a slave's last register/heartbeat
	// before the master marks it stale in the cluster view. Three missed
	// heartbeat intervals.
	slaveStaleAfter = 3 * leaderReconnectInterval
	// defaultServerPort is the default TCP port for the node API listener.
	defaultServerPort = 13420
	// idTimeDivisor truncates the UnixNano component of agent ids to keep
	// them short (unix socket paths have a 104-char limit on macOS).
	idTimeDivisor = 100000
	// defaultReadyTimeout is the default time to wait for an agent
	// subprocess ready handshake.
	defaultReadyTimeout = 5 * time.Second
	// defaultHealthPollInterval is the default interval for polling agent
	// /health endpoints to detect hung processes.
	defaultHealthPollInterval = 30 * time.Second
	// healthPollTimeout is the timeout for a single agent health poll.
	healthPollTimeout = 5 * time.Second
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
	if cfg.ReadyTimeout == 0 {
		cfg.ReadyTimeout = defaultReadyTimeout
	}
	if cfg.HealthPollInterval == 0 {
		cfg.HealthPollInterval = defaultHealthPollInterval
	}
	// Build the project store. When a state dir is configured, persist
	// projects to <stateDir>/projects.json and load any existing state.
	var projects ProjectStore
	if cfg.StateDir != "" {
		projects = newPersistentProjectStore(filepath.Join(cfg.StateDir, "projects.json"))
		if mem, ok := projects.(*memProjectStore); ok {
			if err := mem.loadProjects(); err != nil {
				logrus.WithError(err).Warn("failed to load persisted projects; starting fresh")
			}
		}
	} else {
		projects = newProjectStore()
	}

	// AAP resume tokens persist alongside projects under the state dir.
	resumePath := ""
	if cfg.StateDir != "" {
		resumePath = filepath.Join(cfg.StateDir, "aap-resume.json")
	}

	return &Server{
		cfg:            cfg,
		procs:          make(map[string]*agentProc),
		slaves:         make(map[string]knownSlave),
		bus:            NewEventBus(),
		ctxStore:       newContextStore(cfg.ContextRetention),
		projects:       projects,
		resume:         newResumeStore(resumePath),
		remoteContexts: make(map[string]ExecutionContext),
		aapInvokes:     newAAPInvocationRegistry(),
		now:            time.Now,
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
	// never blocks local operation. The leader client is created here (not
	// in connectLeader) so LeaderAddr() returns the master address as soon
	// as Start returns, even before the first register attempt completes.
	if s.cfg.Mode == ModeSlave && s.cfg.Leader != "" {
		s.leader = newLeaderClient(s.cfg.Leader, s.cfg.NodeID, s.localAddr())
		go s.connectLeader(ctx)
	}

	// Start background health polling for agent subprocesses.
	s.startHealthPolling(ctx)

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

	// s.leader was created in Start() so LeaderAddr() is available
	// immediately; use it here for registration and heartbeats.
	client := s.leader

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
			if err := client.heartbeat(ctx, s.agentNames(), s.localContextDigests()); err != nil {
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
// name must correspond to an agent in the registry (agents.Get) for an ADK
// agent, or a configured AAP agent definition (cfg.AgentDefs).
func (s *Server) SpawnAgent(ctx context.Context, name string) (string, error) {
	return s.spawnAgentWithWorkspace(ctx, name, "")
}

// resolveAgentKind returns the kind of a named agent. A configured AAP def
// wins; otherwise the name is a native ADK agent (registry-built). An unknown
// name with no def and no registry entry returns an error.
func (s *Server) resolveAgentKind(name string) (AgentKind, *AgentDef, error) {
	if def, ok := s.cfg.AgentDefs[name]; ok && def.Kind == AgentKindAAP {
		return AgentKindAAP, &def, nil
	}
	// ADK: validate via the registry so an unknown name fails fast.
	if _, err := agents.Get(name); err != nil {
		return "", nil, fmt.Errorf("verify agent %q: %w", name, err)
	}
	return AgentKindADK, nil, nil
}

// spawnAgentWithWorkspace is like SpawnAgent but passes the workspace path
// to the agent subprocess (advisory filesystem scope). Used by the project
// flows (CreateProject, AssignAgent) where the project's workspace is known.
// It branches on the agent kind: ADK spawns `horde agent`; AAP spawns the
// configured adapter and runs the AAP initialize→ready handshake.
func (s *Server) spawnAgentWithWorkspace(ctx context.Context, name, workspace string) (string, error) {
	kind, aapDef, err := s.resolveAgentKind(name)
	if err != nil {
		return "", err
	}

	s.mu.Lock()
	id := fmt.Sprintf("a%d-%d", s.nextID, time.Now().UnixNano()%idTimeDivisor)
	s.nextID++
	s.mu.Unlock()

	if kind == AgentKindAAP {
		return s.spawnAAP(ctx, id, name, workspace, aapDef)
	}

	// ADK path: the original subprocess + spawn_ready handshake.
	socketPath := s.agentSocketPath(id)

	cmd, cancel, err := s.startAgentProcess(name, socketPath, workspace)
	if err != nil {
		cancel()
		return "", err
	}

	confirmedSocket, readyErr := readReadyHandshake(cmd.stdout, s.cfg.ReadyTimeout)
	if readyErr != nil {
		_ = cmd.Process.Signal(os.Interrupt)
		_ = cmd.Wait()
		cancel()
		_ = os.Remove(socketPath)
		return "", fmt.Errorf("agent %q ready handshake: %w", name, readyErr)
	}

	proc := &agentProc{
		id:         id,
		name:       name,
		kind:       AgentKindADK,
		state:      AgentRunning,
		cmd:        cmd.Cmd,
		doneCh:     make(chan struct{}),
		socketPath: confirmedSocket,
		healthy:    true,
	}

	s.mu.Lock()
	s.procs[id] = proc
	s.mu.Unlock()

	logrus.WithFields(logrus.Fields{
		logKeyAgent: name, "id": id, "socket": confirmedSocket,
	}).Info("agent started")

	s.ctxStore.init(id, s.cfg.NodeID)

	s.trackAgentExit(proc, id, confirmedSocket)

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

// spawnAAP spawns an AAP adapter, runs the handshake, and registers the
// agentProc. AAP agents have no socket path (no HTTP endpoint); the invoke
// path talks to the aapHostSession, not a unix socket.
func (s *Server) spawnAAP(ctx context.Context, id, name, workspace string, def *AgentDef) (string, error) {
	if workspace == "" {
		workspace = "."
	}
	session, cancel, err := newAAPHostSession(ctx, id, name, def, workspace, s.ctxStore, s.resume)
	if err != nil {
		// newAAPHostSession cleans up its own context on failure and returns a
		// nil cancel, so do not call cancel() here (it would nil-panic).
		return "", err
	}
	if err := session.handshake(workspace, s.cfg.ReadyTimeout); err != nil {
		cancel()
		return "", err
	}

	proc := &agentProc{
		id:         id,
		name:       name,
		kind:       AgentKindAAP,
		state:      AgentRunning,
		cmd:        session.cmd,
		doneCh:     make(chan struct{}),
		healthy:    true,
		aapSession: session,
	}

	s.mu.Lock()
	s.procs[id] = proc
	s.mu.Unlock()

	logrus.WithFields(logrus.Fields{
		logKeyAgent: name, "id": id,
		"agent_name": session.ready.Agent.Name,
		"caps":       session.ready.Capabilities,
	}).Info("aap agent started")

	s.ctxStore.init(id, s.cfg.NodeID)
	s.trackAAPAgentExit(proc, id, cancel)

	return id, nil
}

// agentSocketPath returns the unix socket path for a given agent id.
func (s *Server) agentSocketPath(id string) string {
	socketDir := s.cfg.SocketDir
	if socketDir == "" {
		socketDir = os.TempDir()
	}
	return filepath.Join(socketDir, id+".sock")
}

// agentCmd wraps exec.Cmd with a reference to the stdout pipe for the
// ready handshake.
type agentCmd struct {
	*exec.Cmd
	stdout io.ReadCloser
}

// startAgentProcess creates and starts the agent subprocess, returning the
// cmd and a cancel func. The caller is responsible for cancel() on error.
func (s *Server) startAgentProcess(name, socketPath, workspace string) (*agentCmd, func(), error) {
	cmdCtx, cancel := context.WithCancel(context.Background())
	args := []string{"agent", "--name", name, "--socket", socketPath}
	if workspace != "" {
		args = append(args, "--workspace", workspace)
	}
	// AgentCommand is operator-controlled config, not untrusted user input.
	cmd := exec.CommandContext(cmdCtx, s.cfg.AgentCommand, args...) //#nosec G204
	cmd.Stderr = os.Stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, cancel, fmt.Errorf("create stdout pipe for agent %q: %w", name, err)
	}
	cmd.Cancel = func() error {
		_ = cmd.Process.Signal(os.Interrupt)
		return nil
	}
	if err := cmd.Start(); err != nil {
		return nil, cancel, fmt.Errorf("start agent %q: %w", name, err)
	}
	return &agentCmd{Cmd: cmd, stdout: stdout}, cancel, nil
}

// trackAgentExit starts a goroutine that cleans up the agent proc when the
// subprocess exits.
func (s *Server) trackAgentExit(proc *agentProc, id, socketPath string) {
	go func() {
		_ = proc.cmd.Wait()
		s.mu.Lock()
		if p, ok := s.procs[id]; ok {
			p.state = AgentExited
		}
		delete(s.procs, id)
		s.mu.Unlock()
		s.ctxStore.setLifecycle(id, AgentExited)
		close(proc.doneCh)
		_ = os.Remove(socketPath)
		logrus.WithField("id", id).Info("agent exited")
	}()
}

// trackAAPAgentExit starts a goroutine that cleans up an AAP agent proc when
// the adapter subprocess exits. The aapHostSession's doneCh closes when the
// reader sees EOF; this goroutine waits on that, marks the proc exited, and
// mirrors the ADK trackAgentExit cleanup (without a socket to remove).
func (s *Server) trackAAPAgentExit(proc *agentProc, id string, cancel context.CancelFunc) {
	go func() {
		<-proc.aapSession.doneCh
		s.mu.Lock()
		if p, ok := s.procs[id]; ok {
			p.state = AgentExited
		}
		delete(s.procs, id)
		s.mu.Unlock()
		s.ctxStore.setLifecycle(id, AgentExited)
		close(proc.doneCh)
		cancel()
		logrus.WithField("id", id).Info("aap agent exited")
	}()
}

// Agents returns a snapshot of currently running agent subprocesses.
func (s *Server) Agents() []AgentInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]AgentInfo, 0, len(s.procs))
	for _, p := range s.procs {
		out = append(out, AgentInfo{
			ID:      p.id,
			Name:    p.name,
			Status:  p.state,
			Healthy: p.healthy,
			Socket:  p.socketPath,
		})
	}
	return out
}

// AgentInfo describes a running agent.
type AgentInfo struct {
	ID      string
	Name    string
	Status  AgentState
	Healthy bool
	Socket  string
}

// StopAgent signals one agent by id to stop, mirroring Run's shutdown path:
// SIGTERM, then SIGKILL after agentShutdownGrace. For an AAP agent it sends
// an AAP shutdown frame first (graceful), then falls back to the same
// SIGTERM/SIGKILL sequence. It returns an error if the id is unknown or the
// agent has already exited.
func (s *Server) StopAgent(id string) error {
	s.mu.Lock()
	p, ok := s.procs[id]
	if !ok {
		s.mu.Unlock()
		return fmt.Errorf("stop agent %q: %w", id, ErrAgentNotFound)
	}
	p.state = AgentExiting
	s.mu.Unlock()

	// AAP: send a graceful shutdown frame before signaling.
	if p.kind == AgentKindAAP && p.aapSession != nil {
		_ = p.aapSession.shutdown()
	} else if p.cmd.Process != nil {
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

// LeaderAddr returns the master node's address (host:port) when this node is
// a slave with a configured leader, or "" otherwise. Used by the API layer to
// decide whether to forward project requests to the master.
func (s *Server) LeaderAddr() string {
	if s.cfg.Mode != ModeSlave || s.leader == nil {
		return ""
	}
	return s.leader.leaderAddr()
}

// ForwardProjectRequest proxies a project API request to the master node.
// It is called by the API handlers when this node is a slave with a leader.
// Returns the HTTP status code, response headers, response body, and error.
//
//nolint:gocritic // unnamedResult: result types are clear from context
func (s *Server) ForwardProjectRequest(ctx context.Context, method, path string, body []byte) (int, http.Header, []byte, error) {
	if s.leader == nil {
		return 0, nil, nil, fmt.Errorf("no leader configured")
	}
	return s.leader.forwardRequest(ctx, method, path, body)
}

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
	addr     string
	agents   []string
	lastSeen time.Time
}

// SlaveInfo is a snapshot of a registered slave, as surfaced by the cluster
// view (GET /api/v1/cluster/nodes). Stale is computed against slaveStaleAfter
// at snapshot time.
type SlaveInfo struct {
	NodeID   string
	Addr     string
	Agents   []string
	LastSeen time.Time
	Stale    bool
}

// RegisterSlave records a slave's registration with this master. Only
// meaningful in master mode; in slave mode it is a no-op.
func (s *Server) RegisterSlave(nodeID, addr string) {
	if s.cfg.Mode != ModeMaster {
		return
	}
	s.mu.Lock()
	sl := s.slaves[nodeID]
	sl.addr = addr
	sl.lastSeen = s.now()
	s.slaves[nodeID] = sl
	s.mu.Unlock()
	logrus.WithFields(logrus.Fields{"slave": nodeID, "addr": addr}).Debug("slave registered")
}

// Heartbeat records a heartbeat from a slave — refreshing its last-seen time
// and reported agents — and returns the leader's node id and connectivity
// status. Only meaningful in master mode. The contexts payload is the
// slave's redacted execution context digests, stored in the aggregated
// remote view.
func (s *Server) Heartbeat(nodeID string, agentList []string, digests []ExecutionContextDigest) (string, bool) {
	if s.cfg.Mode != ModeMaster {
		return "", false
	}
	s.mu.Lock()
	sl := s.slaves[nodeID]
	sl.lastSeen = s.now()
	sl.agents = agentList
	s.slaves[nodeID] = sl
	s.mu.Unlock()

	// Reconcile the aggregated remote view with this node's reported set.
	// Called unconditionally (even for an empty set) so that agents a slave
	// has dropped are cleared rather than lingering forever.
	ctxs := make([]ExecutionContext, 0, len(digests))
	for i := range digests {
		d := &digests[i]
		ctxs = append(ctxs, ExecutionContext{
			AgentID:              d.AgentID,
			NodeID:               nodeID,
			Project:              d.Project,
			Issue:                d.Issue,
			Activity:             d.Activity,
			WaitingModel:         d.WaitingModel,
			Blocked:              d.Blocked,
			ErrorCount:           d.ErrorCount,
			PendingApprovalCount: d.PendingApprovalCount,
			Lifecycle:            d.Lifecycle,
			UpdatedAt:            d.UpdatedAt,
		})
	}
	s.ReportContexts(nodeID, ctxs)
	return s.cfg.NodeID, true
}

// Slaves returns a snapshot of the slaves registered with this master, each
// marked stale if its last register/heartbeat is older than slaveStaleAfter.
// Empty for a slave node (which keeps no registry).
func (s *Server) Slaves() []SlaveInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	out := make([]SlaveInfo, 0, len(s.slaves))
	for id, sl := range s.slaves {
		out = append(out, SlaveInfo{
			NodeID:   id,
			Addr:     sl.addr,
			Agents:   sl.agents,
			LastSeen: sl.lastSeen,
			Stale:    now.Sub(sl.lastSeen) > slaveStaleAfter,
		})
	}
	return out
}

// agentNames returns the names of the currently running local agents, sent to
// the master on each heartbeat so the cluster view reflects slave workloads.
func (s *Server) agentNames() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	names := make([]string, 0, len(s.procs))
	for _, p := range s.procs {
		names = append(names, p.name)
	}
	return names
}

// AgentSocket returns the unix socket path for the given agent id, or ""
// if the agent is unknown or not yet ready.
func (s *Server) AgentSocket(id string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.procs[id]
	if !ok {
		return ""
	}
	return p.socketPath
}

// IsAgentReady reports whether the agent subprocess has completed its ready
// handshake (i.e. its socket path is populated).
func (s *Server) IsAgentReady(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.procs[id]
	return ok && p.socketPath != ""
}

// IsAAPAgent reports whether the agent with the given id is an AAP adapter
// (vs a native ADK agent). The invoke handler branches on this: an AAP agent
// has no unix socket to reverse-proxy, so the node runs the turn itself via
// AAPInvoke.
func (s *Server) IsAAPAgent(agentID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.procs[agentID]
	return ok && p.kind == AgentKindAAP
}

// RespondApproval resolves a pending tool-use approval for an AAP agent with
// an explicit allow/deny decision. It writes the approval_response frame to
// the adapter and clears the pending ref from the execution context. This is
// the node-as-approval-authority decision path (the API/TUI call it), the
// counterpart to the auto_approve policy in the host session.
//
// It errors if the agent is unknown (ErrAgentNotFound), is a native ADK agent
// (ErrNotAAPAgent), or the request id is not pending (ErrApprovalNotFound).
func (s *Server) RespondApproval(agentID, requestID string, decision aap.ApprovalDecision) error {
	s.mu.Lock()
	proc, ok := s.procs[agentID]
	s.mu.Unlock()
	if !ok {
		return fmt.Errorf("respond approval %q: %w", agentID, ErrAgentNotFound)
	}
	if proc.kind != AgentKindAAP || proc.aapSession == nil {
		return fmt.Errorf("respond approval %q: %w", agentID, ErrNotAAPAgent)
	}
	return proc.aapSession.resolvePending(requestID, decision)
}

// AgentContext returns the execution context for the given agent id, or
// nil if the agent is unknown.
func (s *Server) AgentContext(id string) *ExecutionContext {
	return s.ctxStore.get(id)
}

// AllAgentContexts returns the execution contexts of all local agents.
func (s *Server) AllAgentContexts() []ExecutionContext {
	return s.ctxStore.all()
}

// ContextShareFull reports whether this node exposes full execution context to
// remote principals on its own endpoints (agent.context_share = "full"). When
// false, remote principals receive the redacted subset + counts.
func (s *Server) ContextShareFull() bool {
	return s.cfg.ContextShareFull
}

// SubscribeAgentContext returns a channel that receives execution context
// changes for the given agent id. The cancel func unsubscribes and closes
// the channel.
//
//nolint:gocritic // unnamedResult: result types are clear
func (s *Server) SubscribeAgentContext(id string) (<-chan ExecutionContext, func()) {
	return s.ctxStore.subscribe(id)
}

// ReportContexts is called by a slave during heartbeat to report its
// agents' execution contexts to the master. The master stores them in the
// aggregated remote view. Only meaningful in master mode.
func (s *Server) ReportContexts(nodeID string, contexts []ExecutionContext) {
	if s.cfg.Mode != ModeMaster {
		return
	}
	s.mu.Lock()
	// Replace this node's entries wholesale so agents it no longer reports
	// are removed from the aggregated view (replace-per-node semantics).
	s.evictRemoteNodeLocked(nodeID)
	for i := range contexts {
		key := nodeID + "/" + contexts[i].AgentID
		c := contexts[i]
		c.NodeID = nodeID
		s.remoteContexts[key] = c
	}
	s.mu.Unlock()
}

// RemoteAgentContexts returns the aggregated, redacted execution contexts
// from all slaves. Only non-empty on a master.
func (s *Server) RemoteAgentContexts() []ExecutionContext {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	out := make([]ExecutionContext, 0, len(s.remoteContexts))
	for key, ctx := range s.remoteContexts { //nolint:gocritic // map value copy is fine
		nodeID := key
		if i := strings.IndexByte(key, '/'); i >= 0 {
			nodeID = key[:i]
		}
		// Reap contexts from nodes that have gone stale so a node that stops
		// heartbeating does not linger in the aggregated view. Nodes not (yet)
		// in the slave registry are kept: a heartbeating node is always
		// registered, so "unknown" means pre-registration, not departed.
		if sl, ok := s.slaves[nodeID]; ok && now.Sub(sl.lastSeen) > slaveStaleAfter {
			delete(s.remoteContexts, key)
			continue
		}
		out = append(out, ctx.Redacted())
	}
	return out
}

// EvictRemoteNode removes all remote contexts for the given node id (e.g.
// when the node goes stale).
func (s *Server) EvictRemoteNode(nodeID string) {
	s.mu.Lock()
	s.evictRemoteNodeLocked(nodeID)
	s.mu.Unlock()
}

// evictRemoteNodeLocked removes all remote contexts for nodeID. The caller
// must hold s.mu. The trailing "/" ensures node ids that are prefixes of one
// another (e.g. "slave-1" vs "slave-10") do not collide.
func (s *Server) evictRemoteNodeLocked(nodeID string) {
	prefix := nodeID + "/"
	for key := range s.remoteContexts {
		if strings.HasPrefix(key, prefix) {
			delete(s.remoteContexts, key)
		}
	}
}

// localContextDigests returns the redacted context digests for all local
// agents, for inclusion in the heartbeat payload to the master.
func (s *Server) localContextDigests() []ExecutionContextDigest {
	all := s.ctxStore.all()
	out := make([]ExecutionContextDigest, 0, len(all))
	for _, ctx := range all { //nolint:gocritic // map value copy is fine
		out = append(out, ExecutionContextDigest{
			AgentID:              ctx.AgentID,
			Project:              ctx.Project,
			Issue:                ctx.Issue,
			Activity:             ctx.Activity,
			WaitingModel:         ctx.WaitingModel,
			Blocked:              ctx.Blocked,
			ErrorCount:           len(ctx.Errors),
			PendingApprovalCount: len(ctx.PendingApprovals),
			Lifecycle:            ctx.Lifecycle,
			UpdatedAt:            ctx.UpdatedAt,
		})
	}
	return out
}

// startHealthPolling launches a background goroutine that polls each agent's
// /health endpoint at the configured interval. If an agent fails to respond
// within healthPollTimeout it is marked unhealthy. The goroutine exits when
// ctx is canceled.
func (s *Server) startHealthPolling(ctx context.Context) {
	if s.cfg.HealthPollInterval <= 0 {
		return
	}
	go func() {
		ticker := time.NewTicker(s.cfg.HealthPollInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.pollAgentHealths(ctx)
			}
		}
	}()
}

// pollAgentHealths polls every running agent's /health endpoint.
func (s *Server) pollAgentHealths(ctx context.Context) {
	s.mu.Lock()
	procs := make([]*agentProc, 0, len(s.procs))
	for _, p := range s.procs {
		procs = append(procs, p)
	}
	s.mu.Unlock()

	for _, p := range procs {
		// AAP agents have no HTTP endpoint; the reader goroutine detects
		// process exit directly. Skip HTTP health polling for them.
		if p.kind == AgentKindAAP {
			continue
		}
		if p.socketPath == "" {
			continue
		}
		healthy := s.pollOneAgent(ctx, p.socketPath)
		s.mu.Lock()
		if p2, ok := s.procs[p.id]; ok {
			p2.healthy = healthy
		}
		s.mu.Unlock()
	}
}

// pollOneAgent polls a single agent's /health over its unix socket. Returns
// true if the agent responded with 200.
func (s *Server) pollOneAgent(ctx context.Context, socketPath string) bool {
	client := &http.Client{
		Timeout: healthPollTimeout,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				d := net.Dialer{}
				return d.DialContext(ctx, "unix", socketPath) //#nosec G704 // server-controlled socket path
			},
		},
	}
	pollCtx, cancel := context.WithTimeout(ctx, healthPollTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(pollCtx, http.MethodGet,
		"http://unix/health", http.NoBody)
	if err != nil {
		return false
	}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode == http.StatusOK
}

// readyHandshake is the NDJSON message emitted by the agent subprocess on
// stdout to announce its unix socket is ready.
type readyHandshake struct {
	Type   string `json:"type"`
	Socket string `json:"socket"`
}

// readReadyHandshake reads the first line from the agent subprocess stdout,
// parses it as a spawn_ready JSON message, and returns the socket path. It
// fails if no valid handshake arrives within the timeout.
func readReadyHandshake(r io.Reader, timeout time.Duration) (string, error) {
	type result struct {
		socket string
		err    error
	}
	ch := make(chan result, 1)
	go func() {
		scanner := bufio.NewScanner(r)
		if scanner.Scan() {
			line := scanner.Bytes()
			var msg readyHandshake
			if err := json.Unmarshal(line, &msg); err != nil {
				ch <- result{"", fmt.Errorf("parse ready handshake: %w", err)}
				return
			}
			if msg.Type != "spawn_ready" {
				ch <- result{"", fmt.Errorf("unexpected handshake type %q, want %q", msg.Type, "spawn_ready")}
				return
			}
			if msg.Socket == "" {
				ch <- result{"", errors.New("ready handshake has empty socket path")}
				return
			}
			ch <- result{msg.Socket, nil}
			return
		}
		if err := scanner.Err(); err != nil {
			ch <- result{"", fmt.Errorf("read ready handshake: %w", err)}
			return
		}
		ch <- result{"", errors.New("agent subprocess closed stdout before ready handshake")}
	}()
	select {
	case res := <-ch:
		return res.socket, res.err
	case <-time.After(timeout):
		return "", errors.New("agent subprocess ready handshake timed out")
	}
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
