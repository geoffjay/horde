package server

import (
	"sync"
	"time"

	"github.com/geoffjay/horde/internal/aap"
)

// ActivityState is the agent activity reported via AAP status frames.
// For native ADK agents (no AAP) the node derives a coarse value.
type ActivityState = aap.ActivityState

// Redefine the busy/idle constants for local convenience. They alias the
// AAP values so the wire format is consistent.
const (
	StateBusy = aap.StateBusy
	StateIdle = aap.StateIdle
)

// ErrorSummary is a bounded, non-sensitive projection of an AAP error frame.
type ErrorSummary struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Fatal   bool   `json:"fatal"`
}

// ApprovalRef is a bounded reference to a pending tool-use approval.
type ApprovalRef struct {
	RequestID string `json:"request_id"`
	ToolName  string `json:"tool_name"`
}

// ExecutionContext is the materialized work-state of one agent.
type ExecutionContext struct {
	AgentID string `json:"agent_id"`
	NodeID  string `json:"node_id"`

	// Set at launch or on (re)assignment; empty until Phase 3.5 Slice B.
	Project string `json:"project,omitempty"`
	Issue   string `json:"issue,omitempty"`

	// Runtime state (from AAP context/status frames; coarse for ADK agents).
	Activity      ActivityState `json:"activity"`
	WaitingModel  bool          `json:"waiting_model"`
	Blocked       bool          `json:"blocked"`
	BlockedReason string        `json:"blocked_reason,omitempty"`
	Note          string        `json:"note,omitempty"`

	Errors           []ErrorSummary `json:"errors,omitempty"`
	PendingApprovals []ApprovalRef  `json:"pending_approvals,omitempty"`

	// Counts are the redacted-view projection of the slices above: a remote
	// principal sees how many errors / pending approvals exist without the
	// payloads. They are zero (omitted) in the full local view, where the
	// slices themselves are present.
	ErrorCount           int `json:"error_count,omitempty"`
	PendingApprovalCount int `json:"pending_approval_count,omitempty"`

	Lifecycle AgentState `json:"lifecycle"`
	TurnID    string     `json:"turn_id,omitempty"`
	UpdatedAt time.Time  `json:"updated_at"`
}

// maxErrors is the bound on retained error summaries per agent.
const maxErrors = 10 //nolint:unused // used when AAP host feeds error frames

// maxPendingApprovals is the bound on retained pending approval refs.
const maxPendingApprovals = 20 //nolint:unused // used when AAP host feeds approval frames

// contextStore holds an ExecutionContext per agent id, with change
// subscribers for the SSE stream endpoint.
type contextStore struct {
	mu        sync.Mutex
	retention time.Duration
	ctxs      map[string]*ExecutionContext
	subs      map[string][]chan ExecutionContext
}

func newContextStore(retention time.Duration) *contextStore {
	return &contextStore{
		retention: retention,
		ctxs:      make(map[string]*ExecutionContext),
		subs:      make(map[string][]chan ExecutionContext),
	}
}

// cloneContext returns a deep copy of ctx: the slice fields are copied so the
// returned value shares no backing array with the stored entry (callers read
// it without holding the store lock).
func cloneContext(ctx *ExecutionContext) ExecutionContext {
	c := *ctx
	if ctx.Errors != nil {
		c.Errors = append([]ErrorSummary(nil), ctx.Errors...)
	}
	if ctx.PendingApprovals != nil {
		c.PendingApprovals = append([]ApprovalRef(nil), ctx.PendingApprovals...)
	}
	return c
}

// sendLatest delivers notif to ch, coalescing to the newest value: on a full
// buffer it discards the stale queued value and enqueues notif, so a slow
// subscriber never gets stuck on an out-of-date snapshot (e.g. missing the
// terminal exited state). It never blocks; channels are never closed (cancel
// only unsubscribes), so it never sends on a closed channel.
func sendLatest(ch chan ExecutionContext, notif *ExecutionContext) {
	select {
	case ch <- *notif:
		return
	default:
	}
	select {
	case <-ch:
	default:
	}
	select {
	case ch <- *notif:
	default:
	}
}

// init creates a context entry for the given agent id if one does not exist.
func (cs *contextStore) init(agentID, nodeID string) *ExecutionContext {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	ctx, ok := cs.ctxs[agentID]
	if !ok {
		ctx = &ExecutionContext{
			AgentID:   agentID,
			NodeID:    nodeID,
			Activity:  StateIdle,
			Lifecycle: AgentRunning,
			UpdatedAt: time.Now().UTC(),
		}
		cs.ctxs[agentID] = ctx
	}
	return ctx
}

// get returns a copy of the context for the given agent id, or nil.
func (cs *contextStore) get(agentID string) *ExecutionContext {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	ctx, ok := cs.ctxs[agentID]
	if !ok {
		return nil
	}
	c := cloneContext(ctx)
	return &c
}

// all returns copies of all contexts.
func (cs *contextStore) all() []ExecutionContext {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	out := make([]ExecutionContext, 0, len(cs.ctxs))
	for _, ctx := range cs.ctxs {
		out = append(out, cloneContext(ctx))
	}
	return out
}

// update applies a mutation function to the context for the given agent id
// and notifies subscribers. If the agent has no context entry, the mutation
// is skipped.
func (cs *contextStore) update(agentID string, fn func(*ExecutionContext)) {
	cs.mu.Lock()
	ctx, ok := cs.ctxs[agentID]
	if !ok {
		cs.mu.Unlock()
		return
	}
	fn(ctx)
	ctx.UpdatedAt = time.Now().UTC()
	subs := cs.subs[agentID]
	notif := cloneContext(ctx)
	cs.mu.Unlock()

	for _, ch := range subs {
		sendLatest(ch, &notif)
	}
}

// subscribe returns a channel that receives context changes for the given
// agent id. The cancel func unsubscribes; it deliberately does NOT close the
// channel, because a concurrent update() may still hold a reference to it and
// closing would risk a send-on-closed-channel panic. Readers must stop via
// their own request/context cancellation, not by ranging until close.
//
//nolint:gocritic // unnamedResult: result types are clear
func (cs *contextStore) subscribe(agentID string) (<-chan ExecutionContext, func()) {
	rawCh := make(chan ExecutionContext, 1)
	cs.mu.Lock()
	cs.subs[agentID] = append(cs.subs[agentID], rawCh)
	cs.mu.Unlock()

	cancel := func() {
		cs.mu.Lock()
		defer cs.mu.Unlock()
		subs := cs.subs[agentID]
		for i, s := range subs {
			if s == rawCh {
				cs.subs[agentID] = append(subs[:i], subs[i+1:]...)
				break
			}
		}
		if len(cs.subs[agentID]) == 0 {
			delete(cs.subs, agentID)
		}
	}
	return rawCh, cancel
}

// remove deletes the context entry for the given agent id.
func (cs *contextStore) remove(agentID string) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	delete(cs.ctxs, agentID)
}

// applyStatus merges an AAP status frame into the context.
//
//nolint:unused // used when AAP host feeds status frames
func (cs *contextStore) applyStatus(agentID string, st aap.Status) {
	cs.update(agentID, func(ctx *ExecutionContext) {
		ctx.Activity = st.State
	})
}

// applyContextUpdate merges an AAP context (partial update) frame.
//
//nolint:unused // used when AAP host feeds context frames
func (cs *contextStore) applyContextUpdate(agentID string, cu aap.ContextUpdate) {
	cs.update(agentID, func(ctx *ExecutionContext) {
		if cu.TurnID != nil {
			ctx.TurnID = *cu.TurnID
		}
		if cu.Issue != nil {
			ctx.Issue = *cu.Issue
		}
		if cu.Blocked != nil {
			ctx.Blocked = *cu.Blocked
		}
		if cu.BlockedReason != nil {
			ctx.BlockedReason = *cu.BlockedReason
		}
		if cu.WaitingModel != nil {
			ctx.WaitingModel = *cu.WaitingModel
		}
		if cu.Note != nil {
			ctx.Note = *cu.Note
		}
	})
}

// applyError appends an AAP error frame to the context's error list (bounded).
//
//nolint:unused // used when AAP host feeds error frames
func (cs *contextStore) applyError(agentID string, e aap.Error) {
	cs.update(agentID, func(ctx *ExecutionContext) {
		code := ""
		if e.Code != nil {
			code = *e.Code
		}
		ctx.Errors = append(ctx.Errors, ErrorSummary{
			Code:    code,
			Message: e.Message,
			Fatal:   e.Fatal,
		})
		if len(ctx.Errors) > maxErrors {
			ctx.Errors = ctx.Errors[len(ctx.Errors)-maxErrors:]
		}
	})
}

// applyApprovalRequest adds a pending approval ref.
//
//nolint:unused // used when AAP host feeds approval request frames
func (cs *contextStore) applyApprovalRequest(agentID string, ar aap.ApprovalRequest) {
	cs.update(agentID, func(ctx *ExecutionContext) {
		ctx.PendingApprovals = append(ctx.PendingApprovals, ApprovalRef{
			RequestID: ar.RequestID,
			ToolName:  ar.ToolName,
		})
		if len(ctx.PendingApprovals) > maxPendingApprovals {
			ctx.PendingApprovals = ctx.PendingApprovals[len(ctx.PendingApprovals)-maxPendingApprovals:]
		}
	})
}

// applyApprovalResponse removes a resolved pending approval by request id.
//
//nolint:unused // used when AAP host feeds approval response frames
func (cs *contextStore) applyApprovalResponse(agentID, requestID string) {
	cs.update(agentID, func(ctx *ExecutionContext) {
		for i, a := range ctx.PendingApprovals {
			if a.RequestID == requestID {
				ctx.PendingApprovals = append(ctx.PendingApprovals[:i], ctx.PendingApprovals[i+1:]...)
				break
			}
		}
	})
}

// setLifecycle updates the lifecycle state. When the agent has exited it
// resets transient fields and, if a retention period is configured, schedules
// removal of the entry after that delay so the store does not grow unbounded.
func (cs *contextStore) setLifecycle(agentID string, state AgentState) {
	cs.update(agentID, func(ctx *ExecutionContext) {
		ctx.Lifecycle = state
		if state == AgentExited {
			ctx.Activity = StateIdle
			ctx.Blocked = false
			ctx.WaitingModel = false
			ctx.PendingApprovals = nil
		}
	})
	if state == AgentExited && cs.retention > 0 {
		time.AfterFunc(cs.retention, func() { cs.remove(agentID) })
	}
}

// Redacted returns a copy of the context safe for a remote principal: the
// sensitive fields (blocked_reason, note, error payloads, approval payloads,
// turn_id) are dropped, and errors/approvals are reduced to counts. It is
// idempotent — applied to an already-redacted context (e.g. one rebuilt from a
// heartbeat digest, which carries counts but no slices) it preserves the
// counts rather than recomputing them to zero.
func (c *ExecutionContext) Redacted() ExecutionContext {
	return ExecutionContext{
		AgentID:              c.AgentID,
		NodeID:               c.NodeID,
		Project:              c.Project,
		Issue:                c.Issue,
		Activity:             c.Activity,
		WaitingModel:         c.WaitingModel,
		Blocked:              c.Blocked,
		ErrorCount:           c.errorCount(),
		PendingApprovalCount: c.pendingApprovalCount(),
		Lifecycle:            c.Lifecycle,
		UpdatedAt:            c.UpdatedAt,
	}
}

// errorCount returns the number of errors, preferring the live slice length
// and falling back to the count field (set when rebuilt from a digest).
func (c *ExecutionContext) errorCount() int {
	if len(c.Errors) > 0 {
		return len(c.Errors)
	}
	return c.ErrorCount
}

// pendingApprovalCount mirrors errorCount for pending approvals.
func (c *ExecutionContext) pendingApprovalCount() int {
	if len(c.PendingApprovals) > 0 {
		return len(c.PendingApprovals)
	}
	return c.PendingApprovalCount
}
