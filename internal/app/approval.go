package app

import (
	"context"

	tea "charm.land/bubbletea/v2"
	"github.com/sirupsen/logrus"

	"github.com/geoffjay/horde/internal/client"
)

// approvalAllow and approvalDeny are the decision strings sent to the node's
// approval endpoint (they match aap.DecisionAllow / aap.DecisionDeny).
const (
	approvalAllow = "allow"
	approvalDeny  = "deny"
)

// approvalActionMsg carries the result of an approve/deny decision. On success
// the updated context (with the pending ref removed) arrives via the SSE
// stream; this message only surfaces an error and re-clamps the cursor.
type approvalActionMsg struct {
	err error
}

// moveSelection moves the active selection by delta. On the agent view with
// pending approvals it drives the approval cursor (so up/down choose which
// approval to act on); otherwise it moves the list cursor.
func (m *Model) moveSelection(delta int) {
	if m.view == viewAgent && m.hasPendingApprovals() {
		m.moveApprovalCursor(delta)
		return
	}
	m.moveCursor(delta)
}

// hasPendingApprovals reports whether the selected agent has any pending
// approvals in its (full, local) execution context.
func (m *Model) hasPendingApprovals() bool {
	return m.pendingApprovalCount() > 0
}

// pendingApprovalCount is the number of pending approvals on the selected
// agent's context, or zero when none / no agent is selected.
func (m *Model) pendingApprovalCount() int {
	ctx, ok := m.selectedAgentContext()
	if !ok {
		return 0
	}
	return len(ctx.PendingApprovals)
}

// selectedAgentContext returns the cached execution context for the currently
// selected agent, if any.
func (m *Model) selectedAgentContext() (client.ExecutionContext, bool) {
	a, ok := m.selectedAgent()
	if !ok {
		return client.ExecutionContext{}, false
	}
	ctx, ok := m.contexts[a.ID]
	return ctx, ok
}

// selectedApproval returns the pending approval at the approval cursor, if any.
func (m *Model) selectedApproval() (client.ApprovalRef, bool) {
	ctx, ok := m.selectedAgentContext()
	if !ok || len(ctx.PendingApprovals) == 0 {
		return client.ApprovalRef{}, false
	}
	i := m.approvalCursor
	if i < 0 || i >= len(ctx.PendingApprovals) {
		return client.ApprovalRef{}, false
	}
	return ctx.PendingApprovals[i], true
}

// moveApprovalCursor moves the approval cursor by delta, clamped to the
// pending-approval list.
func (m *Model) moveApprovalCursor(delta int) {
	n := m.pendingApprovalCount()
	if n == 0 {
		m.approvalCursor = 0
		return
	}
	m.approvalCursor += delta
	if m.approvalCursor < 0 {
		m.approvalCursor = 0
	}
	if m.approvalCursor >= n {
		m.approvalCursor = n - 1
	}
}

// clampApprovalCursor keeps the approval cursor within the (possibly shrunk)
// pending-approval list, e.g. after a decision resolves one.
func (m *Model) clampApprovalCursor() {
	n := m.pendingApprovalCount()
	if m.approvalCursor >= n {
		m.approvalCursor = n - 1
	}
	if m.approvalCursor < 0 {
		m.approvalCursor = 0
	}
}

// approvalDecisionCmd returns a tea.Cmd that resolves the selected pending
// approval with the given decision. It returns nil when no approval is
// selected (nothing to decide).
func (m *Model) approvalDecisionCmd(decision string) tea.Cmd {
	a, ok := m.selectedAgent()
	if !ok {
		return nil
	}
	ap, ok := m.selectedApproval()
	if !ok {
		return nil
	}
	agentID := a.ID
	requestID := ap.RequestID
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.ctx, nodeFetchTimeout)
		defer cancel()
		return approvalActionMsg{err: m.c.RespondApproval(ctx, agentID, requestID, decision)}
	}
}

// handleApprovalAction processes an approvalActionMsg: it logs any error and
// re-clamps the approval cursor. The resolved approval is removed from the
// context by the SSE stream, so no local list mutation is needed here.
func (m *Model) handleApprovalAction(msg *approvalActionMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		logrus.WithError(msg.err).Debug("tui: approval decision failed")
	}
	m.clampApprovalCursor()
	return m, nil
}
