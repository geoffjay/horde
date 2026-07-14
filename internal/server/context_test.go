package server

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/geoffjay/horde/internal/aap"
)

func TestContextStore_Init(t *testing.T) {
	cs := newContextStore(0)
	ctx := cs.init("agent-1", "node-1")

	assert.Equal(t, "agent-1", ctx.AgentID)
	assert.Equal(t, "node-1", ctx.NodeID)
	assert.Equal(t, StateIdle, ctx.Activity)
	assert.Equal(t, AgentRunning, ctx.Lifecycle)
	assert.Empty(t, ctx.Project)
	assert.Empty(t, ctx.Issue)

	// Re-init is idempotent.
	ctx2 := cs.init("agent-1", "node-1")
	assert.Equal(t, ctx.AgentID, ctx2.AgentID)
}

func TestContextStore_SetProject(t *testing.T) {
	cs := newContextStore(0)
	cs.init("agent-1", "node-1")

	cs.setProject("agent-1", "proj-1", "issue-42")
	ctx := cs.get("agent-1")
	require.NotNil(t, ctx)
	assert.Equal(t, "proj-1", ctx.Project)
	assert.Equal(t, "issue-42", ctx.Issue)
}

func TestContextStore_ApplyStatus(t *testing.T) {
	cs := newContextStore(0)
	cs.init("agent-1", "node-1")

	cs.applyStatus("agent-1", aap.Status{State: aap.StateBusy})
	ctx := cs.get("agent-1")
	require.NotNil(t, ctx)
	assert.Equal(t, StateBusy, ctx.Activity)
}

func TestContextStore_ApplyContextUpdate_PartialMerge(t *testing.T) {
	cs := newContextStore(0)
	cs.init("agent-1", "node-1")

	blocked := true
	reason := "waiting for review"
	cs.applyContextUpdate("agent-1", aap.ContextUpdate{
		Blocked:       &blocked,
		BlockedReason: &reason,
	})

	ctx := cs.get("agent-1")
	require.NotNil(t, ctx)
	assert.True(t, ctx.Blocked)
	assert.Equal(t, "waiting for review", ctx.BlockedReason)

	// A later frame omitting Blocked should leave the prior value.
	note := "making progress"
	cs.applyContextUpdate("agent-1", aap.ContextUpdate{
		Note: &note,
	})

	ctx = cs.get("agent-1")
	assert.True(t, ctx.Blocked, "blocked should persist from prior frame")
	assert.Equal(t, "waiting for review", ctx.BlockedReason, "blocked_reason should persist")
	assert.Equal(t, "making progress", ctx.Note)
}

func TestContextStore_ApplyError(t *testing.T) {
	cs := newContextStore(0)
	cs.init("agent-1", "node-1")

	code := "E001"
	cs.applyError("agent-1", aap.Error{Code: &code, Message: "something broke"})

	ctx := cs.get("agent-1")
	require.NotNil(t, ctx)
	require.Len(t, ctx.Errors, 1)
	assert.Equal(t, "E001", ctx.Errors[0].Code)
	assert.Equal(t, "something broke", ctx.Errors[0].Message)

	// Errors are bounded.
	for i := 0; i < maxErrors+5; i++ {
		cs.applyError("agent-1", aap.Error{Message: "err"})
	}
	ctx = cs.get("agent-1")
	assert.Len(t, ctx.Errors, maxErrors)
}

func TestContextStore_ApplyApprovalRequest(t *testing.T) {
	cs := newContextStore(0)
	cs.init("agent-1", "node-1")

	cs.applyApprovalRequest("agent-1", aap.ApprovalRequest{
		RequestID: "req-1",
		ToolName:  "bash",
	})

	ctx := cs.get("agent-1")
	require.NotNil(t, ctx)
	require.Len(t, ctx.PendingApprovals, 1)
	assert.Equal(t, "req-1", ctx.PendingApprovals[0].RequestID)
	assert.Equal(t, "bash", ctx.PendingApprovals[0].ToolName)

	// Resolve it.
	cs.applyApprovalResponse("agent-1", "req-1")
	ctx = cs.get("agent-1")
	assert.Empty(t, ctx.PendingApprovals)
}

func TestContextStore_SetLifecycleExited(t *testing.T) {
	cs := newContextStore(0)
	cs.init("agent-1", "node-1")

	cs.applyStatus("agent-1", aap.Status{State: aap.StateBusy})
	cs.setLifecycle("agent-1", AgentExited)

	ctx := cs.get("agent-1")
	require.NotNil(t, ctx)
	assert.Equal(t, AgentExited, ctx.Lifecycle)
	assert.Equal(t, StateIdle, ctx.Activity)
	assert.False(t, ctx.Blocked)
	assert.Empty(t, ctx.PendingApprovals)
}

func TestContextStore_Subscribe(t *testing.T) {
	cs := newContextStore(0)
	cs.init("agent-1", "node-1")

	ch, cancel := cs.subscribe("agent-1")
	defer cancel()

	cs.applyStatus("agent-1", aap.Status{State: aap.StateBusy})

	select {
	case ctx := <-ch:
		assert.Equal(t, StateBusy, ctx.Activity)
	case <-time.After(time.Second):
		t.Fatal("did not receive context change notification")
	}
}

func TestExecutionContext_Redacted(t *testing.T) {
	now := time.Now().UTC()
	ctx := ExecutionContext{
		AgentID:          "agent-1",
		NodeID:           "node-1",
		Project:          "proj-1",
		Issue:            "issue-42",
		Activity:         StateBusy,
		WaitingModel:     true,
		Blocked:          true,
		BlockedReason:    "sensitive reason",
		Note:             "sensitive note",
		Errors:           []ErrorSummary{{Code: "E001", Message: "sensitive error"}},
		PendingApprovals: []ApprovalRef{{RequestID: "req-1", ToolName: "bash"}},
		Lifecycle:        AgentRunning,
		TurnID:           "turn-1",
		UpdatedAt:        now,
	}

	r := ctx.Redacted()

	// Visible fields.
	assert.Equal(t, "agent-1", r.AgentID)
	assert.Equal(t, "node-1", r.NodeID)
	assert.Equal(t, "proj-1", r.Project)
	assert.Equal(t, "issue-42", r.Issue)
	assert.Equal(t, StateBusy, r.Activity)
	assert.True(t, r.WaitingModel)
	assert.True(t, r.Blocked)
	assert.Equal(t, AgentRunning, r.Lifecycle)
	assert.Equal(t, now, r.UpdatedAt)

	// Redacted out.
	assert.Empty(t, r.BlockedReason)
	assert.Empty(t, r.Note)
	assert.Empty(t, r.TurnID)
	// Error/approval payloads are dropped; only counts survive.
	assert.Empty(t, r.Errors)
	assert.Empty(t, r.PendingApprovals)
	assert.Equal(t, 1, r.ErrorCount)
	assert.Equal(t, 1, r.PendingApprovalCount)

	// Redaction is idempotent: re-redacting preserves the counts rather than
	// recomputing them to zero from the now-empty slices.
	r2 := r.Redacted()
	assert.Equal(t, 1, r2.ErrorCount)
	assert.Equal(t, 1, r2.PendingApprovalCount)
}

func TestContextStore_Get_NilForUnknown(t *testing.T) {
	cs := newContextStore(0)
	assert.Nil(t, cs.get("nonexistent"))
}

func TestContextStore_All(t *testing.T) {
	cs := newContextStore(0)
	cs.init("agent-1", "node-1")
	cs.init("agent-2", "node-1")

	all := cs.all()
	assert.Len(t, all, 2)
}

func TestContextStore_Update_SkipsUnknown(t *testing.T) {
	cs := newContextStore(0)
	cs.update("nonexistent", func(ctx *ExecutionContext) {
		ctx.Blocked = true
	})
	assert.Nil(t, cs.get("nonexistent"))
}
