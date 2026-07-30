// Package app implements the horde TUI: a bubbletea program that is the
// primary interface for interacting with the horde system. The TUI is a
// pure client of the node API — it never starts a node in-process and never
// imports internal/server. If no node is reachable at the configured address
// it shows a 60-second retry countdown (with an immediate-retry key) rather
// than spawning one.
package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/sirupsen/logrus"

	"github.com/geoffjay/horde/internal/client"
	"github.com/geoffjay/horde/internal/clientlog"
)

// retryInterval is how long the TUI waits between automatic connection
// retries when no node is reachable.
const retryInterval = 60 * time.Second

// nodeFetchTimeout caps a single node-info/agents fetch.
const nodeFetchTimeout = 10 * time.Second

// agentRefreshInterval is how often the TUI re-fetches agents while connected.
const agentRefreshInterval = 2 * time.Second

// view identifies one screen in the breadcrumb drill-down stack.
type view int

const (
	// viewProjects is the projects home — the default connected view.
	viewProjects view = iota
	// viewProjectDetail is one project's team and per-agent context.
	viewProjectDetail
	// viewAgent is a single agent's live execution context (SSE).
	viewAgent
	// viewInvoke is the multi-turn conversation with an agent.
	viewInvoke
	// viewCluster is the cluster topology (nodes + remote agents).
	viewCluster
	// viewEvents is the live cluster-activity feed (agent lifecycle events).
	viewEvents
	// viewAgents is the top-level list of the node's running agents.
	viewAgents
	// viewLogs is the client-log page: the TUI's own log output, captured
	// into an in-memory buffer instead of being written to the terminal.
	viewLogs
	// viewUsers is the (placeholder) users overview — per-user accounts do not
	// exist yet, so it renders a "not available" notice.
	viewUsers
	// viewTeams is the teams overview: one entry per project, since teams are
	// defined per project (there is no standalone team API).
	viewTeams
	// viewTeamDetail is one project's team roster.
	viewTeamDetail
)

// Model is the bubbletea model for the horde TUI.
type Model struct {
	ctx context.Context
	c   *client.Client

	// connection state
	connected   bool
	retrying    bool
	retryIn     time.Duration // remaining seconds until next auto-retry
	lastAttempt time.Time

	// node + agents
	node   client.NodeInfo
	agents []client.Agent
	// availableAgents are the spawnable agent types (built-in + configured),
	// offered as choices in the new-agent form.
	availableAgents []client.AvailableAgent
	// actionErr holds the last failed action's message (e.g. a spawn error),
	// shown in the body until the next successful action or navigation.
	actionErr string

	// navigation: the current view drives the detail pane. The left sidebar
	// (m.sidebar) selects which entity/feed the detail pane shows; m.focus
	// says whether keys drive the sidebar or the detail pane. detailBack is a
	// short transient drill within the detail pane (project → agent → invoke)
	// that esc pops; it is not a breadcrumb trail and is reset on every
	// sidebar selection.
	view       view
	sidebar    sidebar
	focus      focus
	detailBack []view

	// list cursor within the current detail view (index into the visible list)
	cursor int

	// approvalCursor selects among the selected agent's pending approvals on
	// the agent view (viewAgent). It is independent of cursor (which indexes
	// the team agents) so up/down can drive approval selection without losing
	// the agent selection.
	approvalCursor int

	// selectedProjectID is the project open in the projectDetail/agent/invoke
	// views. Set by pushView when drilling in from the projects list; used to
	// look up the project independently of the cursor (which indexes team
	// agents in projectDetail).
	selectedProjectID string

	// selectedAgentID is set when drilling into invoke from the Agents view,
	// where the agent is a standalone node agent (not a project-team member).
	// selectedAgent() prefers it so the invoke view resolves the right agent.
	selectedAgentID string

	// cached domain state
	projects       []client.Project
	contexts       map[string]client.ExecutionContext
	nodes          client.ClusterView
	remoteContexts []client.ExecutionContext
	// users is the per-user identity list + auth-enabled state for the Users
	// view (best-effort fetch).
	users client.UsersResponse

	// SSE subscription for the agent context stream. When the user drills
	// into the agent view, subscribeAgentContext opens a stream and stores
	// the cancel func here; popView cancels it when leaving. streamConnected
	// drives the "live ●" status-line block.
	streamCancel    context.CancelFunc
	streamConnected bool
	streamCh        <-chan client.ExecutionContext
	streamCtx       context.Context

	// invoke state: the conversation transcript, the text input buffer,
	// and the SSE invoke stream subscription. The transcript accumulates
	// user messages and agent responses (token by token). invokeStreaming
	// drives the "streaming ●" indicator in the invoke view.
	invokeTranscript []transcriptEntry
	invokeInput      string
	invokeStreaming  bool
	invokeCancel     context.CancelFunc
	invokeCh         <-chan client.InvokeEvent
	invokeErr        string // non-empty when the invoke failed (e.g. 409)

	// status line + command palette overlay
	status    *StatusLine
	pal       palette
	picker    listPicker
	form      projectForm
	agentForm agentForm

	// cluster-activity event stream (viewEvents): a bounded ring of recent
	// events fed by the SSE stream. eventsConnected drives the "live ●"
	// indicator; eventsCancel/eventsCh manage the subscription.
	events          []client.Event
	eventsCancel    context.CancelFunc
	eventsCh        <-chan client.Event
	eventsConnected bool

	// logs is the in-memory buffer of this client's own log output, rendered
	// on the logs page (viewLogs). logrus writes here instead of stderr so the
	// TUI display is not corrupted. logScroll offsets the visible window from
	// the tail (0 = pinned to newest).
	logs      *clientlog.Buffer
	logScroll int

	width    int
	height   int
	quitting bool
}

// New constructs the initial Model for the TUI, targeting the node API at
// addr (host:port). token is the per-user bearer token sent on every request
// (empty when auth is disabled). It creates the in-memory client-log buffer
// that the logs page renders; Run redirects logrus into it.
func New(ctx context.Context, addr, token string) *Model {
	m := &Model{
		ctx:      ctx,
		c:        client.New(addr),
		node:     client.NodeInfo{Mode: unknownLabel},
		view:     viewProjects,
		contexts: make(map[string]client.ExecutionContext),
		status:   DefaultStatusLine(),
		logs:     clientlog.NewBuffer(clientlog.DefaultCapacity),
		sidebar:  sidebar{expanded: make(map[groupID]bool)},
		focus:    focusSidebar,
	}
	if token != "" {
		m.c.SetAuth(token)
	}
	// Start with the sidebar cursor on the Projects group so the initial
	// highlight matches the default projects overview in the detail pane.
	m.sidebar.cursor = m.groupRowIndex(groupProjects)
	return m
}

// Init implements tea.Model.
func (m *Model) Init() tea.Cmd {
	return m.connect
}

// --- messages ---

type connectResultMsg struct {
	err error
}

type nodeInfoMsg struct {
	node           client.NodeInfo
	agents         []client.Agent
	available      []client.AvailableAgent
	projects       []client.Project
	contexts       map[string]client.ExecutionContext
	clusterNodes   client.ClusterView
	remoteContexts []client.ExecutionContext
	users          client.UsersResponse
	err            error
}

type tickMsg struct{}

type retryTickMsg struct{}

// transcriptEntry is one line in the invoke conversation transcript.
// Role is "user" (the › prompt) or "agent" (the ● response).
type transcriptEntry struct {
	role string
	text string
}

// contextDeltaMsg carries one execution-context snapshot from the SSE
// stream. The TUI updates m.contexts[agentID] on each delta so the agent
// view renders live state.
type contextDeltaMsg struct {
	ctx client.ExecutionContext
}

// streamErrMsg signals that the agent context SSE stream failed to open
// or ended unexpectedly. The TUI falls back to the cached snapshot.
type streamErrMsg struct {
	err error
}

// invokeEventMsg carries one parsed SSE event from the invoke stream.
type invokeEventMsg struct {
	ev client.InvokeEvent
}

// invokeDoneMsg signals that the invoke stream emitted the done event.
type invokeDoneMsg struct{}

// invokeErrorMsg signals that the invoke stream failed to open or
// returned an error event.
type invokeErrorMsg struct {
	err error
}

// invokeStartedMsg carries the result of opening the invoke stream. The
// stream is opened in a command goroutine (not the Update loop) because
// Client.Invoke blocks until the server sends SSE response headers; doing it
// inline would freeze the whole UI while the turn starts.
type invokeStartedMsg struct {
	ch  <-chan client.InvokeEvent
	err error
}

// --- commands ---

// connect probes the node's /health endpoint. On success it fetches node
// info + agents; on failure it arms the retry countdown.
func (m *Model) connect() tea.Msg {
	err := m.c.Health(m.ctx)
	return connectResultMsg{err: err}
}

// loadNode fetches node metadata, the agent list, the projects, and the
// execution contexts after a successful health check. Errors from
// secondary fetches (projects, contexts) are tolerated — the TUI shows
// what it got and leaves the rest empty.
func (m *Model) loadNode() tea.Msg {
	ctx, cancel := context.WithTimeout(m.ctx, nodeFetchTimeout)
	defer cancel()

	node, nErr := m.c.Node(ctx)
	agents, aErr := m.c.ListAgents(ctx)
	if nErr != nil && aErr != nil {
		return nodeInfoMsg{err: nErr}
	}

	// Available agent types (best-effort): populates the new-agent form.
	available, avErr := m.c.ListAvailableAgents(ctx)
	if avErr != nil {
		logrus.WithError(avErr).Debug("tui: fetch available agents failed")
	}

	// Projects and contexts are best-effort: a node that doesn't expose them
	// (e.g. an older version) should not prevent the TUI from rendering.
	// Errors are logged via logrus so forwarding issues are diagnosable.
	projects, pErr := m.c.ListProjects(ctx, "")
	if pErr != nil {
		logrus.WithError(pErr).Debug("tui: fetch projects failed")
	}
	contexts, cErr := m.c.ListAgentContexts(ctx)
	if cErr != nil {
		logrus.WithError(cErr).Debug("tui: fetch agent contexts failed")
	}
	ctxMap := make(map[string]client.ExecutionContext, len(contexts))
	for i := range contexts {
		ctxMap[contexts[i].AgentID] = contexts[i]
	}

	// Cluster nodes and remote agent contexts are best-effort; on a worker
	// or standalone node the cluster view may be empty.
	clusterNodes, clErr := m.c.ListNodes(ctx)
	if clErr != nil {
		logrus.WithError(clErr).Debug("tui: fetch cluster nodes failed")
	}
	remoteContexts, rErr := m.c.ListRemoteAgentContexts(ctx, "")
	if rErr != nil {
		logrus.WithError(rErr).Debug("tui: fetch remote agent contexts failed")
	}

	// Users + auth-enabled state (best-effort): an older node without the
	// /users endpoint returns an error; the TUI renders a placeholder.
	users, uErr := m.c.Users(ctx)
	if uErr != nil {
		logrus.WithError(uErr).Debug("tui: fetch users failed")
	}

	return nodeInfoMsg{
		node:           node,
		agents:         agents,
		available:      available,
		projects:       projects,
		contexts:       ctxMap,
		clusterNodes:   clusterNodes,
		remoteContexts: remoteContexts,
		users:          users,
	}
}

// Update implements tea.Model.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case connectResultMsg:
		if msg.err == nil {
			m.connected = true
			m.retrying = false
			return m, m.loadNode
		}
		cmd := m.armRetry()
		return m, cmd

	case nodeInfoMsg:
		return m.handleNodeInfo(&msg)

	case tickMsg:
		if !m.connected {
			return m, nil
		}
		return m, m.loadNode

	case retryTickMsg:
		return m.handleRetryTick()

	case tea.KeyPressMsg:
		return m.handleKey(msg)

	case contextDeltaMsg:
		return m.handleContextDelta(&msg)

	case streamErrMsg:
		return m.handleStreamEnd(msg)

	case invokeStartedMsg, invokeEventMsg, invokeDoneMsg, invokeErrorMsg:
		return m.handleInvokeMsg(msg)

	case eventMsg, eventStreamEndMsg, agentActionMsg:
		return m.handleActivityMsg(msg)

	case projectActionMsg:
		return m.handleProjectAction(&msg)

	case approvalActionMsg:
		return m.handleApprovalAction(&msg)
	}

	return m, nil
}

// Key constants used in multiple switch statements — extracted to satisfy
// goconst and make the key bindings grep-able.
const (
	keyQuit      = "ctrl+c"
	keyEsc       = "esc"
	keyEnter     = "enter"
	keyUp        = "up"
	keyDown      = "down"
	keyLeft      = "left"
	keyRight     = "right"
	keyTab       = "tab"
	keyBackspace = "backspace"
	keyCtrlP     = "ctrl+p"
	keyCtrlR     = "ctrl+r"
	keyCtrlQ     = "ctrl+q"
	roleAgent    = "agent"
)

// Project state constants used across views, palette, and lifecycle commands.
const (
	stateActive   = "active"
	statePaused   = "paused"
	stateFinished = "finished"
)

// handleKey dispatches key presses. When the command palette, project
// picker, or form is open all keys are routed to the respective overlay
// handler. Otherwise ctrl+q/ctrl+c quits, ctrl+p opens the palette, ctrl+r
// refreshes (or resumes a paused project in the project detail view),
// ctrl+n opens the new-project form, ctrl+l goes to the cluster view, and
// the arrow keys / enter / esc navigate the breadcrumb drill-down.
// Lifecycle actions (pause, finish, assign) fire via ctrl+X shortcuts in
// the project detail view.
func (m *Model) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.pal.open {
		return m.handlePaletteKey(msg)
	}

	if m.picker.open {
		return m.handlePickerKey(msg)
	}

	if m.form.open {
		return m.handleFormKey(msg)
	}

	if m.agentForm.open {
		return m.handleAgentFormKey(msg)
	}

	switch msg.String() {
	case keyQuit, keyCtrlQ:
		m.quitting = true
		return m, tea.Quit
	case keyCtrlP:
		m.openPalette()
		return m, nil
	case keyCtrlR:
		return m.handleRefreshOrResume()
	case "ctrl+n":
		m.openForm()
		return m, nil
	case "ctrl+l":
		if m.connected {
			return m, m.goCluster() //nolint:gocritic // evalOrder: returning the cmd is the intended pattern
		}
		return m, nil
	}

	if !m.connected {
		return m, nil
	}

	// In the invoke view, keys edit the message input or send — not
	// navigation.
	if m.view == viewInvoke {
		return m.handleInvokeKey(msg)
	}

	// Route by focus zone: the sidebar selects entities/feeds; the detail pane
	// navigates the selected view's list and fires its action keys.
	if m.focus == focusSidebar {
		return m.handleSidebarKey(msg)
	}
	return m.handleDetailKey(msg)
}

// handleDetailKey handles arrow/list/navigation keys while the detail pane has
// focus; view-specific action keys are delegated to handleViewActionKey. esc /
// tab / left pop the transient detail drill, or return focus to the sidebar
// when the drill is empty.
func (m *Model) handleDetailKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case keyEsc, keyTab, keyLeft, "h":
		if cmd, popped := m.popDetail(); popped {
			return m, cmd
		}
		m.focus = focusSidebar
		return m, nil
	case keyEnter, keyRight, "l":
		return m.detailEnter()
	case keyUp, "k":
		m.moveSelection(-1)
		return m, nil
	case keyDown, "j":
		m.moveSelection(1)
		return m, nil
	}
	return m.handleViewActionKey(msg)
}

// handleViewActionKey handles the view-aware action keys: approve/deny (a/d)
// on the agent view and the ctrl+X project lifecycle shortcuts (ctrl+a assign,
// ctrl+s pause/resume, ctrl+f finish) on the project detail view.
func (m *Model) handleViewActionKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "a":
		if m.view == viewAgent {
			return m, m.approvalDecisionCmd(approvalAllow) //nolint:gocritic // evalOrder: returning the cmd is the intended pattern
		}
	case "d":
		if m.view == viewAgent {
			return m, m.approvalDecisionCmd(approvalDeny) //nolint:gocritic // evalOrder: returning the cmd is the intended pattern
		}
	case "ctrl+s":
		if m.view == viewProjectDetail {
			return m.handlePauseOrResume()
		}
	case "ctrl+f":
		if m.view == viewProjectDetail {
			return m, m.finishProjectCmd() //nolint:gocritic // evalOrder: returning the cmd is the intended pattern
		}
	case "ctrl+a":
		if m.view == viewProjectDetail {
			if id := m.selectedProjectIDForAction(); id != "" {
				m.openAgentPicker(id)
			}
			return m, nil
		}
		if m.view == viewAgents {
			if a, ok := m.selectedAgent(); ok {
				m.openAssignProjectPicker(a.ID)
			}
			return m, nil
		}
	}
	return m, nil
}

// handlePauseOrResume toggles between pausing an active project and resuming
// a paused project in the detail view.
func (m *Model) handlePauseOrResume() (tea.Model, tea.Cmd) {
	id := m.selectedProjectIDForAction()
	if id == "" {
		return m, nil
	}
	state := m.actionProjectState(id)
	switch state {
	case stateActive:
		return m, m.pauseProjectCmd() //nolint:gocritic // evalOrder: returning the cmd is the intended pattern
	case statePaused:
		return m, m.resumeProjectCmd() //nolint:gocritic // evalOrder: returning the cmd is the intended pattern
	}
	return m, nil
}

// handleRefreshOrResume handles ctrl+r: when disconnected it triggers an
// immediate retry; when connected in the project detail view with a paused
// project it resumes the project; otherwise it refreshes the node data.
func (m *Model) handleRefreshOrResume() (tea.Model, tea.Cmd) {
	if !m.connected {
		m.retryIn = 0
		return m, m.connect
	}
	if m.view == viewProjectDetail {
		if cmd := m.resumeProjectCmd(); cmd != nil {
			return m, cmd
		}
	}
	return m, m.loadNode
}

// handleRetryTick processes the per-second retry countdown tick. When the
// countdown reaches zero it triggers a reconnect attempt; otherwise it
// decrements the remaining time and re-arms the tick.
func (m *Model) handleRetryTick() (tea.Model, tea.Cmd) {
	if !m.retrying {
		return m, nil
	}
	m.retryIn -= time.Second
	if m.retryIn <= 0 {
		return m, m.connect
	}
	return m, tea.Tick(time.Second, func(time.Time) tea.Msg { return retryTickMsg{} })
}

// handleInvokeKey handles key presses in the invoke view: enter sends the
// message, backspace deletes, esc pops back, and printable keys append
// to the input buffer.
func (m *Model) handleInvokeKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case keyEsc:
		if cmd, popped := m.popDetail(); popped {
			return m, cmd
		}
		m.focus = focusSidebar
		return m, nil
	case keyEnter:
		if m.invokeStreaming || m.invokeInput == "" {
			return m, nil
		}
		if a, ok := m.selectedAgent(); ok {
			return m, m.sendInvoke(a.ID) //nolint:gocritic // evalOrder: returning the cmd is the intended pattern
		}
		return m, nil
	case keyBackspace:
		if m.invokeInput != "" {
			m.invokeInput = m.invokeInput[:len(m.invokeInput)-1]
		}
		return m, nil
	case keyQuit:
		m.quitting = true
		return m, tea.Quit
	}

	// Append printable characters to the input buffer.
	if msg.Text != "" {
		m.invokeInput += msg.Text
	}
	return m, nil
}

// moveCursor moves the list cursor by delta, clamped to the visible list.
func (m *Model) moveCursor(delta int) {
	n := m.listLength()
	if n == 0 {
		m.cursor = 0
		return
	}
	m.cursor += delta
	if m.cursor < 0 {
		m.cursor = 0
	}
	if m.cursor >= n {
		m.cursor = n - 1
	}
}

// listLength returns the number of items in the current view's list.
func (m *Model) listLength() int {
	switch m.view {
	case viewProjects, viewTeams:
		return len(m.projects)
	case viewProjectDetail, viewTeamDetail, viewAgent:
		return len(m.visibleAgents())
	case viewCluster:
		return len(m.nodes.Nodes)
	case viewAgents:
		return len(m.agents)
	}
	return 0
}

// armRetry transitions the model into the retry-countdown state and returns
// a command that ticks once per second to drive the countdown.
func (m *Model) armRetry() tea.Cmd {
	m.connected = false
	m.retrying = true
	m.retryIn = retryInterval
	m.lastAttempt = time.Now()
	return tea.Tick(time.Second, func(time.Time) tea.Msg { return retryTickMsg{} })
}

// handleNodeInfo stores the fetched node info, agents, projects, and
// contexts, then re-arms the periodic refresh tick. On error it arms the
// retry countdown.
func (m *Model) handleNodeInfo(msg *nodeInfoMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		cmd := m.armRetry()
		return m, cmd
	}
	m.node = msg.node
	m.agents = msg.agents
	m.availableAgents = msg.available
	if msg.projects != nil {
		m.projects = msg.projects
	}
	if msg.contexts != nil {
		m.contexts = msg.contexts
	}
	m.nodes = msg.clusterNodes
	m.remoteContexts = msg.remoteContexts
	m.users = msg.users
	return m, tea.Tick(agentRefreshInterval, func(time.Time) tea.Msg { return tickMsg{} })
}

// handleContextDelta stores an SSE context snapshot and re-arms the pump for
// the next event.
func (m *Model) handleContextDelta(msg *contextDeltaMsg) (tea.Model, tea.Cmd) {
	m.contexts[msg.ctx.AgentID] = msg.ctx
	m.streamConnected = true
	return m, m.pumpAgentContext() //nolint:gocritic // evalOrder: returning the cmd is the intended pattern
}

// handleStreamEnd cleans up SSE stream state when the stream closes or
// errors.
func (m *Model) handleStreamEnd(msg streamErrMsg) (tea.Model, tea.Cmd) {
	m.streamConnected = false
	m.streamCh = nil
	if msg.err != nil {
		logrus.WithError(msg.err).Debug("tui: agent context stream ended")
	}
	return m, nil
}

// handleInvokeEnd cleans up invoke stream state when the stream closes
// (done) or errors. It accepts either invokeDoneMsg or invokeErrorMsg.
func (m *Model) handleInvokeEnd(msg tea.Msg) (tea.Model, tea.Cmd) {
	m.invokeStreaming = false
	m.invokeCh = nil
	if em, ok := msg.(invokeErrorMsg); ok && em.err != nil {
		m.invokeErr = em.err.Error()
	}
	return m, nil
}

// Token events append to the agent's current transcript entry (or start
// a new one). Other event types (invocation) are ignored for rendering.
// The pump is re-armed for the next event.
func (m *Model) handleInvokeEvent(msg invokeEventMsg) (tea.Model, tea.Cmd) {
	switch msg.ev.Type {
	case "token":
		var tok client.InvokeToken
		if json.Unmarshal(msg.ev.Data, &tok) != nil {
			return m, m.pumpInvoke() //nolint:gocritic // evalOrder: returning the cmd is the intended pattern
		}
		// Append to the last agent entry, or start a new one.
		if n := len(m.invokeTranscript); n > 0 && m.invokeTranscript[n-1].role == roleAgent {
			m.invokeTranscript[n-1].text += tok.Text
		} else {
			m.invokeTranscript = append(m.invokeTranscript, transcriptEntry{role: "agent", text: tok.Text})
		}
	case "invocation":
		// The invocation event carries the invocation id; no rendering
		// needed — the transcript captures the conversation.
	}
	return m, m.pumpInvoke() //nolint:gocritic // evalOrder: returning the cmd is the intended pattern
}

// subscribeAgentContext opens an SSE context stream for the given agent.
// It returns a tea.Cmd that reads the first event; Update re-issues
// pumpAgentContext to read subsequent events. The stream runs until
// unsubscribeAgentContext cancels it.
func (m *Model) subscribeAgentContext(agentID string) tea.Cmd {
	m.unsubscribeAgentContext()
	m.approvalCursor = 0

	ctx, cancel := context.WithCancel(m.ctx)
	m.streamCancel = cancel
	m.streamConnected = false

	// Open the stream in the cmd goroutine; stash the channel on the
	// model via a closure variable that pumpAgentContext reads.
	ch, err := m.c.StreamAgentContext(ctx, agentID)
	if err != nil {
		cancel()
		m.streamCancel = nil
		return func() tea.Msg { return streamErrMsg{err: err} }
	}
	m.streamCh = ch
	m.streamCtx = ctx
	return m.pumpAgentContext()
}

// pumpAgentContext returns a tea.Cmd that reads one event from the SSE
// channel and returns it as a contextDeltaMsg. When the channel is closed
// (stream ended), it returns a streamErrMsg so Update can clean up.
func (m *Model) pumpAgentContext() tea.Cmd {
	return func() tea.Msg {
		if m.streamCh == nil {
			return nil
		}
		ec, ok := <-m.streamCh
		if !ok {
			return streamErrMsg{err: nil}
		}
		return contextDeltaMsg{ctx: ec}
	}
}

// unsubscribeAgentContext cancels the active SSE context stream, if any.
func (m *Model) unsubscribeAgentContext() {
	if m.streamCancel != nil {
		m.streamCancel()
		m.streamCancel = nil
	}
	m.streamCh = nil
	m.streamCtx = nil
	m.streamConnected = false
}

// sendInvoke posts the user's input message to the agent and opens the
// SSE invoke stream. It appends the user's message to the transcript,
// clears the input buffer, and returns a tea.Cmd that reads the first
// event. The agent's response accumulates in the transcript as token
// events arrive.
func (m *Model) sendInvoke(agentID string) tea.Cmd {
	msg := m.invokeInput
	m.invokeInput = ""
	m.invokeTranscript = append(m.invokeTranscript, transcriptEntry{role: "user", text: msg})
	m.invokeErr = ""
	m.unsubscribeInvoke()
	m.invokeStreaming = true

	ctx, cancel := context.WithCancel(m.ctx)
	m.invokeCancel = cancel

	// Open the stream in the command goroutine: Client.Invoke blocks until the
	// server flushes SSE headers, so calling it inline would freeze the UI
	// (no key — including ctrl+c — is processed while Update is blocked).
	req := client.InvokeRequest{Message: msg}
	return func() tea.Msg {
		ch, err := m.c.Invoke(ctx, agentID, req)
		return invokeStartedMsg{ch: ch, err: err}
	}
}

// handleActivityMsg routes the cluster-activity stream messages and the
// new-agent spawn result. Grouped into one Update case to keep Update's
// cyclomatic complexity in check.
func (m *Model) handleActivityMsg(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case eventMsg:
		return m.handleEventMsg(msg)
	case eventStreamEndMsg:
		return m.handleEventStreamEnd(msg)
	case agentActionMsg:
		return m.handleAgentAction(&msg)
	}
	return m, nil
}

// handleInvokeMsg routes the invoke-stream lifecycle messages (open result,
// each event, and stream end).
func (m *Model) handleInvokeMsg(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case invokeStartedMsg:
		return m.handleInvokeStarted(msg)
	case invokeEventMsg:
		return m.handleInvokeEvent(msg)
	default: // invokeDoneMsg, invokeErrorMsg
		return m.handleInvokeEnd(msg)
	}
}

// handleInvokeStarted stores the opened invoke stream (or surfaces the open
// error) and begins pumping events.
func (m *Model) handleInvokeStarted(msg invokeStartedMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		if m.invokeCancel != nil {
			m.invokeCancel()
			m.invokeCancel = nil
		}
		m.invokeStreaming = false
		m.invokeErr = msg.err.Error()
		return m, nil
	}
	m.invokeCh = msg.ch
	return m, m.pumpInvoke() //nolint:gocritic // evalOrder: returning the cmd is the intended pattern
}

// pumpInvoke returns a tea.Cmd that reads one event from the invoke SSE
// channel and returns it as an invokeEventMsg (or invokeDoneMsg /
// invokeErrorMsg when the stream ends).
func (m *Model) pumpInvoke() tea.Cmd {
	return func() tea.Msg {
		if m.invokeCh == nil {
			return invokeDoneMsg{}
		}
		ev, ok := <-m.invokeCh
		if !ok {
			return invokeErrorMsg{err: nil}
		}
		switch ev.Type {
		case "done":
			return invokeDoneMsg{}
		case "error":
			var ie client.InvokeError
			_ = json.Unmarshal(ev.Data, &ie)
			return invokeErrorMsg{err: fmt.Errorf("%s: %s", ie.Code, ie.Message)}
		default:
			return invokeEventMsg{ev: ev}
		}
	}
}

// unsubscribeInvoke cancels the active invoke SSE stream, if any.
func (m *Model) unsubscribeInvoke() {
	if m.invokeCancel != nil {
		m.invokeCancel()
		m.invokeCancel = nil
	}
	m.invokeCh = nil
	m.invokeStreaming = false
}

// resetInvokeState clears the transcript and input, called when entering
// or leaving the invoke view.
func (m *Model) resetInvokeState() {
	m.unsubscribeInvoke()
	m.invokeTranscript = nil
	m.invokeInput = ""
	m.invokeStreaming = false
	m.invokeErr = ""
}

// View implements tea.Model.
func (m *Model) View() tea.View {
	if m.quitting {
		return altView("Shutting down horde...\n")
	}

	background := m.fill(m.renderBody(), m.status.Render(m, m.innerWidth()))
	if !m.overlayOpen() {
		return altView(background)
	}

	// Palette or form overlay: dim the whole background (it was rendered
	// plain — see paint) and composite the dialog centered on top of it.
	dimmed := lipgloss.NewStyle().Faint(true).Foreground(dimColor).Render(background)
	dialog := m.renderOverlay()
	x, y := m.dialogOffset(dialog)
	comp := lipgloss.NewCompositor(
		lipgloss.NewLayer(dimmed),
		lipgloss.NewLayer(dialog).X(x).Y(y).Z(1),
	)
	return altView(comp.Render())
}

// renderOverlay returns the dialog to composite over the dimmed background.
// It dispatches to the palette, picker, or form renderer based on which
// overlay is open.
func (m *Model) renderOverlay() string {
	if m.pal.open {
		return m.renderPalette()
	}
	if m.picker.open {
		return m.renderPicker()
	}
	if m.form.open {
		return m.renderForm()
	}
	if m.agentForm.open {
		return m.renderAgentForm()
	}
	return ""
}

// overlayOpen reports whether any modal overlay (palette, picker, project
// form, or agent form) is open, so the background is dimmed and key input is
// routed to that overlay.
func (m *Model) overlayOpen() bool {
	return m.pal.open || m.picker.open || m.form.open || m.agentForm.open
}

// altView wraps content in a full-window (alternate-screen) view. The TUI
// always runs in the alt screen: the frame is a fixed full-terminal-height
// buffer, so the top-flush / full-height layout is anchored to the terminal
// and trailing blank rows (the bottom edge inset) actually render — unlike
// inline mode, which trims trailing blank lines.
func altView(content string) tea.View {
	v := tea.NewView(content)
	v.AltScreen = true
	return v
}

// dimColor is the foreground applied to the whole background while the command
// palette overlay is open.
var dimColor = lipgloss.Color("240")

// paint applies a style's render function to s, except while the command
// palette overlay is open, when it returns s unstyled. This lets View dim the
// entire background with a single faint wrapper: because the background carries
// no inner color/reset escapes, the wrapper applies uniformly. Callers pass a
// style's bound Render method (e.g. someStyle.Render) so the heavy Style struct
// is not copied by value.
func (m *Model) paint(render func(...string) string, s string) string {
	if m.overlayOpen() {
		return s
	}
	return render(s)
}

// edgePad is the one-cell breathing room applied on the left, right, and
// bottom edges of the view. The top is intentionally flush, both because it
// reads fine and because padding it would complicate overlay positioning.
const edgePad = 1

// innerWidth is the usable content width after reserving the left/right edge
// padding.
func (m *Model) innerWidth() int {
	return max(m.width-edgePad-edgePad, 0)
}

// fill lays out the view so it occupies the full terminal height: the body is
// pinned to the top, the footer to the bottom, and the gap between them is
// padded with blank lines. The whole block is then inset by edgePad on the
// left, right, and bottom. Before the first WindowSizeMsg arrives (height 0)
// it falls back to a fixed single-line separator.
func (m *Model) fill(body, footer string) string {
	body = strings.TrimRight(body, "\n")
	if m.height <= 0 {
		return body + "\n\n" + footer + "\n"
	}
	// Reserve the bottom edge row (added by the padding below); the top stays
	// flush. Joining with N newlines yields N-1 blank rows between body and
	// footer, hence the +1 so the block is exactly m.height-edgePad rows tall.
	gap := m.height - edgePad - lipgloss.Height(body) - lipgloss.Height(footer) + 1
	if gap < 1 {
		gap = 1
	}
	inner := body + strings.Repeat("\n", gap) + footer
	return lipgloss.NewStyle().Padding(0, edgePad, edgePad, edgePad).Render(inner)
}

// renderBody builds the main content area (everything above the footer): the
// "horde" title plus either the retry panel or the two-pane
// sidebar-and-detail layout. Node mode/leader live in the status line.
func (m *Model) renderBody() string {
	var b strings.Builder
	title := m.paint(lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("212")).Render, "horde")
	b.WriteString(title + "\n\n")

	if !m.connected {
		b.WriteString(renderRetry(m))
		return b.String()
	}

	b.WriteString(m.renderPanes())
	return b.String()
}

// titleRows is the number of rows the title block occupies (the "horde" line
// plus a blank line below it), reserved when sizing the two panes.
const titleRows = 2

// dividerCols is the number of columns the pane divider occupies (" │ "): the
// bar plus a spacer on each side.
const dividerCols = 3

// sidebarMinDetail is the minimum detail-pane width the sidebar yields to on a
// narrow terminal before the sidebar itself is shrunk.
const sidebarMinDetail = 6

// renderPanes lays out the connected body as two full-height columns — the
// navigation sidebar and the detail pane — separated by a vertical divider.
// Both columns are padded to a common height H so lipgloss.JoinHorizontal
// aligns them; H is sized so the footer stays pinned by fill() (H = terminal
// height − bottom edge − footer − title block).
func (m *Model) renderPanes() string {
	footerH := lipgloss.Height(m.status.Render(m, m.innerWidth()))
	h := m.height - edgePad - footerH - titleRows
	if h < 1 {
		h = 1
	}

	inner := m.innerWidth()
	sw := sidebarWidth
	if sw > inner-sidebarMinDetail {
		sw = max(inner-sidebarMinDetail, 1)
	}
	// The divider occupies dividerCols columns (" │ "): a space on each side of
	// the bar, produced by the JoinHorizontal spacers plus the bar column itself.
	dw := inner - sw - dividerCols
	if dw < 1 {
		dw = 1
	}

	sidebar := lipgloss.NewStyle().Width(sw).Height(h).Render(m.renderSidebar())
	divider := m.paint(lipgloss.NewStyle().Faint(true).Render, dividerColumn(h))
	detail := lipgloss.NewStyle().Width(dw).Height(h).Render(m.renderDetail())

	return lipgloss.JoinHorizontal(lipgloss.Top, sidebar, " ", divider, " ", detail)
}

// dividerColumn returns a single-column vertical bar h rows tall, joining the
// sidebar and detail panes.
func dividerColumn(h int) string {
	if h < 1 {
		h = 1
	}
	lines := make([]string, h)
	for i := range lines {
		lines[i] = "│"
	}
	return strings.Join(lines, "\n")
}

// renderDetail renders the detail-pane content: the last action error (if any)
// followed by the current view's renderer.
func (m *Model) renderDetail() string {
	var b strings.Builder
	if m.actionErr != "" {
		b.WriteString(m.paint(lipgloss.NewStyle().Foreground(lipgloss.Color("203")).Render, "  "+m.actionErr) + "\n\n")
	}
	b.WriteString(m.renderView())
	return b.String()
}

// renderView dispatches to the current view's renderer.
func (m *Model) renderView() string {
	switch m.view {
	case viewProjects:
		return m.renderProjectsView()
	case viewProjectDetail:
		return m.renderProjectDetailView()
	case viewAgent:
		return m.renderAgentView()
	case viewInvoke:
		return m.renderInvokeView()
	case viewCluster:
		return m.renderClusterView()
	case viewEvents:
		return m.renderEventsView()
	case viewAgents:
		return m.renderAgentsView()
	case viewLogs:
		return m.renderLogsView()
	case viewUsers:
		return m.renderUsersView()
	case viewTeams:
		return m.renderTeamsView()
	case viewTeamDetail:
		return m.renderTeamDetailView()
	}
	return ""
}

// renderRetry builds the "no server available" panel shown while the TUI
// waits to retry.
func renderRetry(m *Model) string {
	addr := m.c.BaseURL()
	secs := int(m.retryIn.Seconds())
	var b strings.Builder
	warnStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("203")).Bold(true)
	b.WriteString(m.paint(warnStyle.Render, "No horde node available"))
	b.WriteString("\n\n")
	fmt.Fprintf(&b, "The TUI could not reach a node at %s.\n", addr)
	fmt.Fprintf(&b, "Retrying in %ds...\n", secs)
	return b.String()
}

// Run launches the horde TUI. It blocks until the user quits.
//
// The TUI renders to stderr, so logrus is redirected into the model's
// in-memory buffer (surfaced on the logs page) rather than the terminal. When
// tee is non-nil (log.output "file") the same lines are also written there.
// The buffer's notify callback asks the program to redraw as new lines arrive,
// so the logs page stays live.
func Run(ctx context.Context, addr, token string, tee io.Writer) error {
	m := New(ctx, addr, token)

	out := io.Writer(m.logs)
	if tee != nil {
		out = io.MultiWriter(m.logs, tee)
	}
	logrus.SetOutput(out)

	p := tea.NewProgram(m, tea.WithOutput(os.Stderr))

	// Emit the startup line before wiring the notify callback. It is captured
	// into the buffer (SetOutput above) but must not trigger a Send yet: the
	// program's message channel is unbuffered and has no reader until p.Run()
	// starts its event loop, so a synchronous Send here would deadlock and the
	// TUI would never render.
	logrus.WithField("addr", addr).Info("launching horde TUI")

	// Request a redraw as new log lines arrive, sending from a goroutine.
	// p.Send blocks until the event loop reads the message, so calling it
	// inline would deadlock whenever a log write originates from the loop's own
	// goroutine (e.g. logging inside Update). Redraw requests are idempotent,
	// so coalesced or reordered sends are harmless; once p.Run() returns its
	// context is canceled and any in-flight sends unblock and exit.
	m.logs.SetNotify(func() { go p.Send(logsUpdatedMsg{}) })

	_, err := p.Run()

	// Detach the buffer from the program: once Run returns the program can no
	// longer receive sends, and logrus should go back to stderr.
	m.logs.SetNotify(nil)
	logrus.SetOutput(os.Stderr)

	if err != nil {
		return fmt.Errorf("tui run: %w", err)
	}
	return nil
}
